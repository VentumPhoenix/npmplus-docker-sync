package main

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"sort"
	"strings"

	"github.com/VentumPhoenix/npmplus-docker-sync/internal/docker"
)

// composeDocument is the part of `docker compose config --format json` this
// tool cares about.
//
// Reading the rendered configuration rather than the YAML itself keeps the
// binary free of a YAML dependency *and* gets variable interpolation,
// `extends` and multiple `-f` files resolved by compose, which a hand-written
// parser would get subtly wrong.
type composeDocument struct {
	Services map[string]struct {
		ContainerName string          `json:"container_name"`
		Labels        json.RawMessage `json:"labels"`
	} `json:"services"`
}

// validateFile checks the labels of a rendered compose configuration.
//
//	docker compose config --format json > stack.json
//	npmplus-docker-sync validate stack.json
//
// Nothing about the runtime is known here - no addresses, no exposed ports -
// so the check is about the labels alone.
func validateFile(path string, opts docker.ParseOptions, log *slog.Logger) error {
	if log == nil {
		log = slog.Default()
	}
	raw, err := os.ReadFile(path) //nolint:gosec // an operator supplied path
	if err != nil {
		return fmt.Errorf("read %s: %w", path, err)
	}

	var document composeDocument
	if err := json.Unmarshal(raw, &document); err != nil {
		return fmt.Errorf("%s is not a rendered compose configuration "+
			"(create it with: docker compose config --format json): %w", path, err)
	}
	if len(document.Services) == 0 {
		return fmt.Errorf("%s contains no services", path)
	}

	containers := make([]docker.Container, 0, len(document.Services))
	names := make([]string, 0, len(document.Services))
	for name := range document.Services {
		names = append(names, name)
	}
	sort.Strings(names)

	for _, name := range names {
		service := document.Services[name]
		labels, err := decodeLabels(service.Labels)
		if err != nil {
			return fmt.Errorf("service %s: %w", name, err)
		}
		container := name
		if service.ContainerName != "" {
			container = service.ContainerName
		}
		containers = append(containers, docker.Container{
			ID: container, Name: container, Labels: labels, State: "running",
		})
	}

	opts.Offline = true
	snapshot, res := docker.Scan(containers, opts)
	return reportTo(log, snapshot, res)
}

// decodeLabels accepts both shapes compose allows: a mapping and a list of
// "key=value" strings.
func decodeLabels(raw json.RawMessage) (map[string]string, error) {
	if len(raw) == 0 {
		return nil, nil
	}

	var mapping map[string]any
	if err := json.Unmarshal(raw, &mapping); err == nil {
		out := make(map[string]string, len(mapping))
		for key, value := range mapping {
			out[key] = fmt.Sprint(value)
		}
		return out, nil
	}

	var list []string
	if err := json.Unmarshal(raw, &list); err != nil {
		return nil, fmt.Errorf("labels are neither a mapping nor a list: %w", err)
	}
	out := make(map[string]string, len(list))
	for _, entry := range list {
		key, value, _ := strings.Cut(entry, "=")
		out[strings.TrimSpace(key)] = strings.TrimSpace(value)
	}
	return out, nil
}

// report prints what the labels produced and fails on a broken definition.
func report(snapshot docker.Snapshot, res docker.Result) error {
	return reportTo(slog.Default(), snapshot, res)
}

// reportTo is report with an explicit logger.
func reportTo(log *slog.Logger, snapshot docker.Snapshot, res docker.Result) error {
	for _, warning := range res.Warnings {
		log.Warn(warning)
	}
	for _, target := range snapshot.Targets {
		log.Info("resource",
			slog.String("container", target.ContainerName),
			slog.String("kind", string(target.Kind)),
			slog.Int("index", target.Index),
			slog.String("key", target.Key()),
			slog.Bool("running", target.Running))
	}
	for _, problem := range res.Errors {
		log.Error(problem.Error())
	}

	log.Info("validation finished",
		slog.Int("containers", snapshot.Containers),
		slog.Int("managed", snapshot.Summary.Managed),
		slog.Int("resources", len(snapshot.Targets)),
		slog.Int("warnings", len(res.Warnings)),
		slog.Int("errors", len(res.Errors)))
	if len(res.Errors) > 0 {
		return fmt.Errorf("%d invalid label definition(s)", len(res.Errors))
	}
	return nil
}
