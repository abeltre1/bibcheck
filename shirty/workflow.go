// Copyright 2025 National Technology and Engineering Solutions of Sandia
// SPDX-License-Identifier: BSD-3-Clause
package shirty

import (
	"time"

	"github.com/sandialabs/bibcheck/openai"
)

// DefaultModel is the model used for shirty chat completions when none is
// configured via WithModel.
const DefaultModel = "meta-llama/Llama-3.3-70B-Instruct"

// DefaultTimeout bounds how long a single shirty request waits for a response.
// It is generous because large models can be slow to respond (or cold-loading)
// behind the proxy. Override it with WithTimeout.
const DefaultTimeout = 180 * time.Second

type Workflow struct {
	apiKey    string
	baseUrl   string
	model     string
	oaiClient *openai.Client
}

type WorkflowOpt func(*Workflow)

func NewWorkflow(apiKey string, options ...WorkflowOpt) *Workflow {
	c := &Workflow{
		apiKey:  apiKey,
		baseUrl: "https://shirty.sandia.gov/api/v1",
		model:   DefaultModel,
		oaiClient: openai.NewClient(
			apiKey,
			openai.WithBaseUrl("https://shirty.sandia.gov/api/v1"),
			openai.WithTimeout(DefaultTimeout),
		),
	}
	for _, o := range options {
		o(c)
	}
	return c
}

func WithBaseUrl(baseUrl string) WorkflowOpt {
	return func(w *Workflow) {
		w.baseUrl = baseUrl
		openai.WithBaseUrl(baseUrl)(w.oaiClient)
	}
}

// WithModel overrides the chat-completion model used for every request the
// workflow makes. An empty value is ignored so the default model is preserved.
func WithModel(model string) WorkflowOpt {
	return func(w *Workflow) {
		if model == "" {
			return
		}
		w.model = model
	}
}

// WithTimeout overrides how long a single request waits for a response. A
// non-positive value is ignored so the default timeout is preserved.
func WithTimeout(d time.Duration) WorkflowOpt {
	return func(w *Workflow) {
		if d <= 0 {
			return
		}
		openai.WithTimeout(d)(w.oaiClient)
	}
}

func WithAuditEnabled(enabled bool) WorkflowOpt {
	return func(w *Workflow) {
		openai.WithAuditEnabled(enabled)(w.oaiClient)
	}
}

func (w *Workflow) OpenAIClient() *openai.Client {
	return w.oaiClient
}
