// talon-whisper-plugin is a Go subprocess plugin offering an
// `audio_transcribe` tool backed by OpenAI's Whisper API. Native
// counterpart to the openclaw-bundled openai-whisper-api skill;
// same intent (turn audio into text) without the Node runtime.
//
// Wire shape:
//
//   - Refuses to start without TALON_PLUGIN_HANDSHAKE +
//     TALON_PLUGIN_AUTH_COOKIE.
//   - Listens on 127.0.0.1:0, prints "1|TCP|<addr>|grpc" on stdout.
//   - Initialize: offers_tools=[{name: "audio_transcribe", schema:
//     {audio_path, language?, prompt?, response_format?}}].
//   - RunTool: streams the file via multipart/form-data to
//     https://api.openai.com/v1/audio/transcriptions, returns the
//     transcript text. API key from OPENAI_API_KEY env or
//     OPENAI_API_KEY_REF (op:// / keychain:// resolved through
//     talon-op-plugin / talon-keychain-plugin).
//
// Build:
//
//	go build -o bin/talon-whisper-plugin ./apps/talon-whisper-plugin

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"mime/multipart"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"google.golang.org/grpc"

	talonlog "github.com/guygrigsby/talon/internal/log"
	pb "github.com/guygrigsby/talon/internal/plugin/pb"
)

const (
	handshakeMagic = "talon.plugin.v1"

	whisperEndpoint = "https://api.openai.com/v1/audio/transcriptions"

	// 25MB is OpenAI's hard cap; refuse early so the model gets a
	// useful error instead of waiting through a long upload that
	// will be rejected anyway.
	maxUploadBytes = 25 * 1024 * 1024
	httpTimeout    = 5 * time.Minute
)

type whisperPlugin struct {
	pb.UnimplementedPluginServer
	http *http.Client
}

func (s *whisperPlugin) Initialize(_ context.Context, _ *pb.InitializeRequest) (*pb.InitializeResponse, error) {
	return &pb.InitializeResponse{
		Manifest: &pb.Manifest{
			Name:        "talon-whisper",
			Version:     "0.1.0",
			Description: "OpenAI Whisper API transcription (Go plugin)",
			OffersTools: []*pb.ToolSpec{{
				Name: "audio_transcribe",
				Description: "Transcribe an audio file using OpenAI's Whisper API. " +
					"Accepts mp3/mp4/mpeg/mpga/m4a/wav/webm up to 25MB. " +
					"Returns the transcript text. Path must be inside the agent's workspace " +
					"(the host's read tool can put files there if you fetched them via URL).",
				ParametersSchema: []byte(`{
					"type": "object",
					"properties": {
						"audio_path":      {"type": "string", "description": "Path to the audio file (absolute or workspace-relative)."},
						"language":        {"type": "string", "description": "ISO-639-1 hint (en, es, fr, ...). Auto-detected when omitted."},
						"prompt":          {"type": "string", "description": "Optional context to bias the model (e.g. proper nouns / jargon)."},
						"response_format": {"type": "string", "enum": ["text", "json", "srt", "verbose_json", "vtt"], "description": "Output format. Defaults to text."}
					},
					"required": ["audio_path"],
					"additionalProperties": false
				}`),
			}},
		},
	}, nil
}

func (s *whisperPlugin) RunTool(ctx context.Context, req *pb.RunToolRequest) (*pb.RunToolResponse, error) {
	if req.GetToolName() != "audio_transcribe" {
		return &pb.RunToolResponse{
			Output:  fmt.Sprintf("whisper plugin: unknown tool %q", req.GetToolName()),
			IsError: true,
		}, nil
	}
	var args struct {
		AudioPath      string `json:"audio_path"`
		Language       string `json:"language"`
		Prompt         string `json:"prompt"`
		ResponseFormat string `json:"response_format"`
	}
	if err := json.Unmarshal([]byte(req.GetArgumentsJson()), &args); err != nil {
		return errResp("audio_transcribe: invalid arguments JSON: " + err.Error()), nil
	}
	args.AudioPath = strings.TrimSpace(args.AudioPath)
	if args.AudioPath == "" {
		return errResp("audio_transcribe: audio_path is required"), nil
	}
	if args.ResponseFormat == "" {
		args.ResponseFormat = "text"
	}

	// Pre-flight: file exists, size under cap. Cheaper than
	// uploading 25MB to find out OpenAI rejects it.
	st, err := os.Stat(args.AudioPath)
	if err != nil {
		return errResp("audio_transcribe: stat " + args.AudioPath + ": " + err.Error()), nil
	}
	if st.IsDir() {
		return errResp("audio_transcribe: " + args.AudioPath + " is a directory, expected an audio file"), nil
	}
	if st.Size() > maxUploadBytes {
		return errResp(fmt.Sprintf("audio_transcribe: file is %dMB, exceeds OpenAI's 25MB Whisper cap; split or compress first", st.Size()/1024/1024)), nil
	}

	apiKey := resolveOpenAIAPIKey(ctx)
	if apiKey == "" {
		return errResp("audio_transcribe: OpenAI API key missing. Set OPENAI_API_KEY env, or pass an op://... reference via OPENAI_API_KEY_REF."), nil
	}

	body, err := s.callWhisper(ctx, apiKey, args.AudioPath, args.Language, args.Prompt, args.ResponseFormat)
	if err != nil {
		return errResp("audio_transcribe: " + err.Error()), nil
	}
	// `text` format returns plain string; the others return JSON.
	// For JSON, we extract the `text` field for the model to read,
	// but keep the rest available if it asks for response_format
	// explicitly (we pass it through verbatim).
	if args.ResponseFormat == "text" {
		return &pb.RunToolResponse{Output: strings.TrimSpace(string(body))}, nil
	}
	return &pb.RunToolResponse{Output: string(body)}, nil
}

// callWhisper builds + sends the multipart upload, returns the
// raw response body. Caller decides how to format.
func (s *whisperPlugin) callWhisper(ctx context.Context, apiKey, audioPath, language, prompt, format string) ([]byte, error) {
	f, err := os.Open(audioPath)
	if err != nil {
		return nil, fmt.Errorf("open: %w", err)
	}
	defer f.Close()

	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	// Required fields
	if err := mw.WriteField("model", "whisper-1"); err != nil {
		return nil, err
	}
	if err := mw.WriteField("response_format", format); err != nil {
		return nil, err
	}
	if language != "" {
		if err := mw.WriteField("language", language); err != nil {
			return nil, err
		}
	}
	if prompt != "" {
		if err := mw.WriteField("prompt", prompt); err != nil {
			return nil, err
		}
	}
	// File part
	fw, err := mw.CreateFormFile("file", filepath.Base(audioPath))
	if err != nil {
		return nil, err
	}
	if _, err := io.Copy(fw, f); err != nil {
		return nil, fmt.Errorf("upload body: %w", err)
	}
	if err := mw.Close(); err != nil {
		return nil, err
	}

	runCtx, cancel := context.WithTimeout(ctx, httpTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(runCtx, http.MethodPost, whisperEndpoint, &buf)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("Content-Type", mw.FormDataContentType())

	resp, err := s.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}
	if resp.StatusCode/100 != 2 {
		return nil, fmt.Errorf("whisper http %d: %s", resp.StatusCode, truncate(string(respBody), 256))
	}
	return respBody, nil
}

// resolveOpenAIAPIKey: env first, then env-supplied reference
// (resolved via talon-op-plugin / talon-keychain-plugin). Same
// pattern as talon-brave-plugin.
func resolveOpenAIAPIKey(ctx context.Context) string {
	if v := strings.TrimSpace(os.Getenv("OPENAI_API_KEY")); v != "" {
		return v
	}
	ref := strings.TrimSpace(os.Getenv("OPENAI_API_KEY_REF"))
	if ref == "" {
		return ""
	}
	scheme := ""
	switch {
	case strings.HasPrefix(ref, "op://"):
		scheme = "op"
	case strings.HasPrefix(ref, "keychain://"):
		scheme = "keychain"
	default:
		return ref
	}
	binary := "talon-" + scheme + "-plugin"
	path, err := exec.LookPath(binary)
	if err != nil {
		fallback := "/usr/local/bin/" + binary
		if _, statErr := os.Stat(fallback); statErr == nil {
			path = fallback
		} else {
			return ""
		}
	}
	runCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(runCtx, path, ref)
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimRight(string(out), "\r\n")
}

func errResp(msg string) *pb.RunToolResponse {
	return &pb.RunToolResponse{Output: msg, IsError: true}
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

func main() {
	talonlog.Init(talonlog.ParseFormat(os.Getenv("TALON_LOG_FORMAT")))
	log := slog.With("plugin", "whisper")

	if got := os.Getenv("TALON_PLUGIN_HANDSHAKE"); got != handshakeMagic {
		log.Error("handshake env mismatch — refusing to start outside the host",
			"got", got, "want", handshakeMagic)
		os.Exit(1)
	}
	if os.Getenv("TALON_PLUGIN_AUTH_COOKIE") == "" {
		log.Error("missing TALON_PLUGIN_AUTH_COOKIE")
		os.Exit(1)
	}

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		log.Error("listen failed", "err", err)
		os.Exit(1)
	}

	server := grpc.NewServer()
	plug := &whisperPlugin{http: &http.Client{Timeout: httpTimeout + 30*time.Second}}
	pb.RegisterPluginServer(server, plug)

	fmt.Printf("1|TCP|%s|grpc\n", listener.Addr().String())

	if err := server.Serve(listener); err != nil {
		log.Error("grpc serve failed", "err", err)
		os.Exit(1)
	}
}
