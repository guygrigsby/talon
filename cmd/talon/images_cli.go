package main

// `talon images` debug surface for the image studio RPCs landed in
// talon-8z0 + the img2img/styles/manager work shipped after. Mostly
// useful for testing the gateway RPC plumbing from the terminal
// without firing up the Studio UI; power users can also script
// against it (e.g. batch uploads, dump installed style list to a
// file).
//
// Subcommands intentionally stop short of `images generate` /
// `images delete` — those have a richer surface (event streaming,
// run-id tracking) that's better-served by the Studio UI than a
// one-shot CLI. Add those if a clear use case shows up.

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"
)

func imagesCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "images",
		Short: "Inspect + manage talon image studio surfaces",
	}
	c.AddCommand(imagesWorkflowsCmd())
	c.AddCommand(imagesStylesCmd())
	c.AddCommand(imagesManagerCmd())
	c.AddCommand(imagesUploadCmd())
	return c
}

// --- workflows -----------------------------------------------------------

func imagesWorkflowsCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "workflows",
		Short: "List available image workflows (builtins + user dir)",
	}
	c.AddCommand(&cobra.Command{
		Use:   "list",
		Short: "List workflows",
		RunE: func(_ *cobra.Command, _ []string) error {
			payload, err := runRPC("images.workflows.list", nil)
			if err != nil {
				return err
			}
			if flagJSON {
				emit(payload)
				return nil
			}
			var r struct {
				Workflows []struct {
					ID          string `json:"id"`
					Label       string `json:"label"`
					Description string `json:"description"`
					Source      string `json:"source"`
				} `json:"workflows"`
			}
			if err := json.Unmarshal(payload, &r); err != nil {
				return fmt.Errorf("decode: %w", err)
			}
			renderWorkflowsTable(r.Workflows)
			return nil
		},
	})
	return c
}

func renderWorkflowsTable[T any](workflows []T) {
	if len(workflows) == 0 {
		fmt.Println("(no workflows)")
		return
	}
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	defer w.Flush()
	fmt.Fprintln(w, "SOURCE\tID\tLABEL")
	// Use json round-trip to read fields generically without depending
	// on a specific concrete type — keeps this rendering helper
	// reusable across callsites with slightly different envelope shapes.
	raw, _ := json.Marshal(workflows)
	var rows []map[string]any
	_ = json.Unmarshal(raw, &rows)
	for _, row := range rows {
		fmt.Fprintf(w, "%s\t%s\t%s\n",
			fmt.Sprint(row["source"]),
			fmt.Sprint(row["id"]),
			fmt.Sprint(row["label"]),
		)
	}
}

// --- styles --------------------------------------------------------------

func imagesStylesCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "styles",
		Short: "List image-style presets",
	}
	c.AddCommand(&cobra.Command{
		Use:   "list",
		Short: "List style presets",
		RunE: func(_ *cobra.Command, _ []string) error {
			payload, err := runRPC("images.styles.list", nil)
			if err != nil {
				return err
			}
			if flagJSON {
				emit(payload)
				return nil
			}
			var r struct {
				Styles []struct {
					ID          string `json:"id"`
					Label       string `json:"label"`
					Description string `json:"description"`
					Denoise     float64 `json:"denoise"`
					Source      string  `json:"source"`
					Lora        *struct {
						Filename string `json:"filename"`
					} `json:"lora,omitempty"`
				} `json:"styles"`
			}
			if err := json.Unmarshal(payload, &r); err != nil {
				return fmt.Errorf("decode: %w", err)
			}
			if len(r.Styles) == 0 {
				fmt.Println("(no styles)")
				return nil
			}
			w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
			defer w.Flush()
			fmt.Fprintln(w, "SOURCE\tID\tDENOISE\tLORA\tLABEL")
			for _, s := range r.Styles {
				lora := "—"
				if s.Lora != nil && s.Lora.Filename != "" {
					lora = s.Lora.Filename
				}
				fmt.Fprintf(w, "%s\t%s\t%.2f\t%s\t%s\n", s.Source, s.ID, s.Denoise, lora, s.Label)
			}
			return nil
		},
	})
	return c
}

// --- manager -------------------------------------------------------------

func imagesManagerCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "manager",
		Short: "Probe ComfyUI-Manager for presence and queue installs",
	}
	c.AddCommand(&cobra.Command{
		Use:   "status",
		Short: "Report whether ComfyUI-Manager is detected on the active ComfyUI",
		RunE: func(_ *cobra.Command, _ []string) error {
			payload, err := runRPC("images.manager.status", nil)
			if err != nil {
				return err
			}
			if flagJSON {
				emit(payload)
				return nil
			}
			var r struct {
				Present  bool   `json:"present"`
				Endpoint string `json:"endpoint"`
			}
			if err := json.Unmarshal(payload, &r); err != nil {
				return fmt.Errorf("decode: %w", err)
			}
			if r.Present {
				fmt.Printf("✓ detected (endpoint: %s)\n", r.Endpoint)
			} else {
				fmt.Println("✗ not detected — install ComfyUI-Manager to enable click-to-install")
			}
			return nil
		},
	})

	var (
		modelType string
		url       string
		filename  string
		savePath  string
	)
	install := &cobra.Command{
		Use:   "install",
		Short: "Queue a model install via ComfyUI-Manager",
		RunE: func(_ *cobra.Command, _ []string) error {
			body := map[string]any{
				"type": modelType,
				"url":  url,
			}
			if filename != "" {
				body["filename"] = filename
			}
			if savePath != "" {
				body["savePath"] = savePath
			}
			payload, err := runRPC("images.manager.install", body)
			if err != nil {
				return err
			}
			if flagJSON {
				emit(payload)
				return nil
			}
			var r struct {
				OK      bool   `json:"ok"`
				Message string `json:"message"`
			}
			if err := json.Unmarshal(payload, &r); err != nil {
				return fmt.Errorf("decode: %w", err)
			}
			if r.OK {
				if r.Message != "" {
					fmt.Printf("✓ install queued: %s\n", r.Message)
				} else {
					fmt.Println("✓ install queued")
				}
			} else {
				fmt.Println("✗ manager rejected install")
			}
			return nil
		},
	}
	install.Flags().StringVar(&modelType, "type", "loras", "model type (loras / checkpoints / vae / embeddings / etc.)")
	install.Flags().StringVar(&url, "url", "", "download URL (required)")
	install.Flags().StringVar(&filename, "filename", "", "target filename on disk (defaults to URL tail)")
	install.Flags().StringVar(&savePath, "save-path", "", "subdir under models/ (defaults to type-named)")
	_ = install.MarkFlagRequired("url")
	c.AddCommand(install)
	return c
}

// --- upload --------------------------------------------------------------

func imagesUploadCmd() *cobra.Command {
	var (
		filename  string
		subfolder string
		imgType   string
		overwrite bool
	)
	c := &cobra.Command{
		Use:   "upload <path>",
		Short: "Upload a local image to ComfyUI's input dir",
		Long: `Reads a local image file, base64-encodes the bytes, and posts to the
gateway's images.upload RPC, which proxies ComfyUI's /upload/image.
Returns the resolved filename ComfyUI stored it as — useful for
img2img workflows that reference the upload by name in a LoadImage
node override.`,
		Args: cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			path := args[0]
			body, err := os.ReadFile(path)
			if err != nil {
				return fmt.Errorf("read %s: %w", path, err)
			}
			name := filename
			if name == "" {
				name = filepath.Base(path)
			}
			ct := http.DetectContentType(body)
			req := map[string]any{
				"filename":    name,
				"contentType": ct,
				"base64":      base64.StdEncoding.EncodeToString(body),
				"overwrite":   overwrite,
			}
			if subfolder != "" {
				req["subfolder"] = subfolder
			}
			if imgType != "" {
				req["type"] = imgType
			}
			payload, err := runRPC("images.upload", req)
			if err != nil {
				return err
			}
			if flagJSON {
				emit(payload)
				return nil
			}
			var r struct {
				Filename  string `json:"filename"`
				Subfolder string `json:"subfolder"`
				Type      string `json:"type"`
			}
			if err := json.Unmarshal(payload, &r); err != nil {
				return fmt.Errorf("decode: %w", err)
			}
			loc := r.Filename
			if r.Subfolder != "" {
				loc = r.Subfolder + "/" + r.Filename
			}
			fmt.Printf("✓ uploaded as %s (type=%s)\n", loc, strings.ToLower(r.Type))
			return nil
		},
	}
	c.Flags().StringVar(&filename, "filename", "", "target filename on ComfyUI (defaults to source basename)")
	c.Flags().StringVar(&subfolder, "subfolder", "", "subdir under the chosen type's directory")
	c.Flags().StringVar(&imgType, "type", "", "input / temp / output (default input)")
	c.Flags().BoolVar(&overwrite, "overwrite", false, "clobber a same-named file rather than auto-suffix")
	return c
}
