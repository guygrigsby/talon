package chatdriver

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/guygrigsby/jess/tool"

	"github.com/guygrigsby/talon/internal/agentcontext"
)

// finishOnboardingTool is the dedicated first-run setup tool. It exists
// because a fresh install's main agent has no read/write/edit tools
// (those are gated on a configured workspace), so onboarding can't rely
// on raw filesystem access. The tool takes structured identity + user
// fields, writes IDENTITY.md and USER.md, and clears the BOOTSTRAP
// sentinel. Attached only while onboarding is active (see BuildAgent).
type finishOnboardingTool struct {
	dir string
}

// Compile-time assertion that finishOnboardingTool satisfies the jess
// tool.Tool interface.
var _ tool.Tool = (*finishOnboardingTool)(nil)

func newFinishOnboardingTool(dir string) *finishOnboardingTool {
	return &finishOnboardingTool{dir: dir}
}

func (t *finishOnboardingTool) Name() string { return "finish_onboarding" }

func (t *finishOnboardingTool) Description() string {
	return "Complete first-run setup. Call this once you've interviewed the user and know who you should be and who they are. Writes IDENTITY.md and USER.md from the values you pass and clears the onboarding state. agentName is required; pass whatever else you learned."
}

func (t *finishOnboardingTool) Schema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"agentName": map[string]any{
				"type":        "string",
				"description": "The name the user wants to call you",
			},
			"creature": map[string]any{
				"type":        "string",
				"description": "What kind of assistant you are, e.g. 'automation raven'",
			},
			"vibe": map[string]any{
				"type":        "string",
				"description": "Your personality and tone in a few words",
			},
			"emoji": map[string]any{
				"type":        "string",
				"description": "An emoji that represents you",
			},
			"avatar": map[string]any{
				"type":        "string",
				"description": "Optional avatar: workspace-relative path, URL, or data URI",
			},
			"userName": map[string]any{
				"type":        "string",
				"description": "The user's name",
			},
			"userCall": map[string]any{
				"type":        "string",
				"description": "What you should call the user",
			},
			"userTimezone": map[string]any{
				"type":        "string",
				"description": "The user's timezone, e.g. America/Denver",
			},
			"userNotes": map[string]any{
				"type":        "string",
				"description": "How the user likes to work; preferences worth remembering",
			},
		},
		"required": []string{"agentName"},
	}
}

type onboardingArgs struct {
	AgentName    string `json:"agentName"`
	Creature     string `json:"creature"`
	Vibe         string `json:"vibe"`
	Emoji        string `json:"emoji"`
	Avatar       string `json:"avatar"`
	UserName     string `json:"userName"`
	UserCall     string `json:"userCall"`
	UserTimezone string `json:"userTimezone"`
	UserNotes    string `json:"userNotes"`
}

func (t *finishOnboardingTool) Execute(_ context.Context, args json.RawMessage) (json.RawMessage, error) {
	var a onboardingArgs
	if err := json.Unmarshal(args, &a); err != nil {
		return nil, fmt.Errorf("finish_onboarding: bad arguments: %w", err)
	}
	fields := agentcontext.PersonaFields{
		AgentName:    a.AgentName,
		Creature:     a.Creature,
		Vibe:         a.Vibe,
		Emoji:        a.Emoji,
		Avatar:       a.Avatar,
		UserName:     a.UserName,
		UserCall:     a.UserCall,
		UserTimezone: a.UserTimezone,
		UserNotes:    a.UserNotes,
	}
	if err := agentcontext.ApplyOnboarding(t.dir, fields); err != nil {
		return nil, err
	}
	out, _ := json.Marshal(map[string]any{
		"status":  "onboarded",
		"name":    a.AgentName,
		"written": []string{"IDENTITY.md", "USER.md"},
		"message": "Onboarding complete. Identity saved. Carry on the conversation as " + a.AgentName + ".",
	})
	return out, nil
}
