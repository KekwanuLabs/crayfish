// Package skills implements the Crayfish skill system — composable behaviors
// defined in YAML that teach the agent how to use tools in specific ways.
//
// Skills are the primary way to extend Crayfish without writing Go code.
// A skill is a combination of: triggers, prompt engineering, and tool chains.
package skills

import (
	"time"
)

// SkillType defines what kind of skill this is.
type SkillType string

const (
	// TypePrompt augments the system prompt based on context.
	TypePrompt SkillType = "prompt"

	// TypeWorkflow executes a multi-step tool chain.
	TypeWorkflow SkillType = "workflow"

	// TypeReactive triggers on bus events (e.g., new email).
	TypeReactive SkillType = "reactive"
)

// Skill is a loadable behavior definition.
type Skill struct {
	// Identity
	Name        string `yaml:"name" json:"name"`
	Version     int    `yaml:"version" json:"version"`
	Description string `yaml:"description" json:"description"`
	Author      string `yaml:"author,omitempty" json:"author,omitempty"`
	Category    string `yaml:"category,omitempty" json:"category,omitempty"`

	// Marketplace (future-proofing — inert fields, no logic yet).
	License string `yaml:"license,omitempty" json:"license,omitempty"` // "free" (default), "premium", "trial"
	Price   string `yaml:"price,omitempty" json:"price,omitempty"`     // Display price, e.g., "$2.99/mo"

	// Enabled controls whether this skill is active. nil = true (default enabled).
	Enabled *bool `yaml:"enabled,omitempty" json:"enabled,omitempty"`

	// Type determines how the skill is executed.
	Type SkillType `yaml:"type" json:"type"`

	// Trigger defines when this skill activates.
	Trigger Trigger `yaml:"trigger" json:"trigger"`

	// MinTier is the minimum trust tier required to use this skill.
	// Uses the same tier names as tools: "unknown", "group", "trusted", "operator".
	MinTier string `yaml:"min_tier" json:"min_tier"`

	// Steps define the tool chain for workflow/reactive skills.
	Steps []Step `yaml:"steps,omitempty" json:"steps,omitempty"`

	// Prompt is the LLM prompt template. Supports {{variable}} interpolation.
	// For prompt skills: replaces or augments system prompt.
	// For workflow/reactive: used after tool results are collected.
	Prompt string `yaml:"prompt" json:"prompt"`

	// Vars defines default variable values that can be overridden at runtime.
	Vars map[string]string `yaml:"vars,omitempty" json:"vars,omitempty"`

	// Source tracks where this skill was loaded from.
	Source string `yaml:"-" json:"source,omitempty"` // "builtin", "user", filepath
}

// Trigger defines when a skill activates.
type Trigger struct {
	// Command is a slash command like "/briefing" or "/pair".
	Command string `yaml:"command,omitempty" json:"command,omitempty"`

	// Schedule is a cron expression like "0 7 * * *" (7 AM daily).
	Schedule string `yaml:"schedule,omitempty" json:"schedule,omitempty"`

	// Event is a bus event type like "email.new" or "message.inbound".
	Event string `yaml:"event,omitempty" json:"event,omitempty"`

	// Condition is an optional filter expression for event triggers.
	// Simple expressions: "subject contains 'URGENT'"
	Condition string `yaml:"condition,omitempty" json:"condition,omitempty"`

	// Keywords are phrases in natural language that trigger this skill.
	// e.g., ["check my email", "any new mail", "email summary"]
	Keywords []string `yaml:"keywords,omitempty" json:"keywords,omitempty"`
}

// Step defines a single tool invocation in a workflow.
type Step struct {
	// Tool is the name of the tool to call (e.g., "email_check", "web_search").
	Tool string `yaml:"tool" json:"tool"`

	// Params are the input parameters for the tool. Supports {{variable}} interpolation.
	Params map[string]interface{} `yaml:"params,omitempty" json:"params,omitempty"`

	// StoreAs saves the tool result in a variable for use in subsequent steps or the prompt.
	StoreAs string `yaml:"store_as,omitempty" json:"store_as,omitempty"`

	// OnError defines what to do if the tool fails: "skip" (default), "abort", "retry".
	OnError string `yaml:"on_error,omitempty" json:"on_error,omitempty"`
}

// ScheduledRun tracks a scheduled skill execution.
type ScheduledRun struct {
	SkillName string
	NextRun   time.Time
	Schedule  string // cron expression
}

// SkillResult holds the output of a skill execution.
type SkillResult struct {
	SkillName   string            `json:"skill_name"`
	Success     bool              `json:"success"`
	StepResults map[string]string `json:"step_results,omitempty"` // store_as → result
	FinalPrompt string            `json:"final_prompt,omitempty"`
	Error       string            `json:"error,omitempty"`
	Duration    time.Duration     `json:"duration_ms"`
}
