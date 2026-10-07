package models

import (
	"context"
	"net/http"
	"regexp"
)

// Instance is one agent-reported replica of a model.
type Instance struct {
	Actual string `json:"actual"`
}

// Model is a catalog entry from GET /api/models.
type Model struct {
	ID        string     `json:"id"`
	Kind      string     `json:"kind"`
	Instances []Instance `json:"instances"`
}

// Running reports whether any instance of the model is currently running.
func (m Model) Running() bool {
	for _, in := range m.Instances {
		if in.Actual == "running" {
			return true
		}
	}
	return false
}

// Catalog is the decoded GET /api/models response.
type Catalog struct {
	Models []Model `json:"models"`
}

// Catalog fetches the model catalog.
func (c *Client) Catalog(ctx context.Context) (*Catalog, error) {
	var cat Catalog
	if err := c.do(ctx, http.MethodGet, "/api/models", "", nil, &cat); err != nil {
		return nil, err
	}
	return &cat, nil
}

// GroundingModels returns catalog models of kind "grounding".
func (c *Catalog) GroundingModels() []Model { return c.byKind("grounding") }

// ChatModels returns catalog models of kind "chat".
func (c *Catalog) ChatModels() []Model { return c.byKind("chat") }

func (c *Catalog) byKind(kind string) []Model {
	if c == nil {
		return nil
	}
	var out []Model
	for _, m := range c.Models {
		if m.Kind == kind {
			out = append(out, m)
		}
	}
	return out
}

// DefaultGrounding returns a running grounding model's id, falling back to the
// built-in default when none is running (or none is catalogued).
func (c *Catalog) DefaultGrounding() string {
	for _, m := range c.GroundingModels() {
		if m.Running() {
			return m.ID
		}
	}
	return DefaultGroundingModel
}

// plannerPreference mirrors the /cua harness: a running chat model whose id
// looks like a general instruction-follower is preferred.
var plannerPreference = regexp.MustCompile(`(?i)qwen|instruct|chat`)

// DefaultPlanner returns the planner the /cua harness would pick: a running chat
// model preferring an id matching qwen|instruct|chat, else the first running
// chat model, else the first chat model, else "".
func (c *Catalog) DefaultPlanner() string {
	chats := c.ChatModels()
	var firstRunning string
	for _, m := range chats {
		if !m.Running() {
			continue
		}
		if plannerPreference.MatchString(m.ID) {
			return m.ID
		}
		if firstRunning == "" {
			firstRunning = m.ID
		}
	}
	if firstRunning != "" {
		return firstRunning
	}
	if len(chats) > 0 {
		return chats[0].ID
	}
	return ""
}
