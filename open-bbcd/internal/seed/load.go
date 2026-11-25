package seed

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// Load reads a seed file, or every *.yaml / *.yml file directly inside a
// directory (non-recursive, lexical order), and merges them into one Set.
func Load(path string) (*Set, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	files := []string{path}
	if info.IsDir() {
		files = nil
		entries, err := os.ReadDir(path)
		if err != nil {
			return nil, err
		}
		for _, e := range entries {
			ext := strings.ToLower(filepath.Ext(e.Name()))
			if e.IsDir() || (ext != ".yaml" && ext != ".yml") {
				continue
			}
			files = append(files, filepath.Join(path, e.Name()))
		}
		sort.Strings(files)
		if len(files) == 0 {
			return nil, fmt.Errorf("%s: no *.yaml or *.yml files", path)
		}
	}

	merged := &Set{Format: FormatV1}
	backendSrc := map[string]string{}
	agentSrc := map[string]string{}
	versionSrc := map[string]string{}
	var errs []error
	for _, f := range files {
		s, err := loadFile(f)
		if err != nil {
			return nil, err
		}
		for _, b := range s.Backends {
			if prev, ok := backendSrc[b.Name]; ok {
				errs = append(errs, fmt.Errorf("%s: backend %s: duplicate (also in %s)", f, b.Name, prev))
				continue
			}
			backendSrc[b.Name] = f
			merged.Backends = append(merged.Backends, b)
		}
		for _, a := range s.Agents {
			if prev, ok := agentSrc[a.StaticID]; ok {
				errs = append(errs, fmt.Errorf("%s: agent %s: duplicate (also in %s)", f, a.StaticID, prev))
				continue
			}
			agentSrc[a.StaticID] = f
			merged.Agents = append(merged.Agents, a)
		}
		for _, v := range s.Versions {
			if prev, ok := versionSrc[v.StaticID]; ok {
				errs = append(errs, fmt.Errorf("%s: version %s: duplicate (also in %s)", f, v.StaticID, prev))
				continue
			}
			versionSrc[v.StaticID] = f
			merged.Versions = append(merged.Versions, v)
		}
	}
	if err := errors.Join(errs...); err != nil {
		return nil, err
	}
	return merged, nil
}

func loadFile(path string) (*Set, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	var s Set
	if err := dec.Decode(&s); err != nil {
		if errors.Is(err, io.EOF) {
			return nil, fmt.Errorf("%s: empty file (missing format: %s)", path, FormatV1)
		}
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if err := noFurtherDocuments(dec); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if s.Format != FormatV1 {
		return nil, fmt.Errorf("%s: unsupported format %q (want %q)", path, s.Format, FormatV1)
	}
	for i := range s.Backends {
		s.Backends[i].Source = path
		s.Backends[i].Config = normalizeMap(s.Backends[i].Config)
	}
	for i := range s.Agents {
		a := &s.Agents[i]
		a.Source = path
		a.Architecture = normalizeMap(a.Architecture)
		a.FlowMapConfig = normalizeMap(a.FlowMapConfig)
	}
	for i := range s.Versions {
		s.Versions[i].Source = path
		s.Versions[i].Prompts = normalizeMap(s.Versions[i].Prompts)
	}
	return &s, nil
}

// noFurtherDocuments fails if the stream holds another non-empty YAML
// document: a seed file is exactly one document, and a second one would
// otherwise be silently ignored. An empty trailing document ("---" at the end)
// is harmless.
func noFurtherDocuments(dec *yaml.Decoder) error {
	for {
		var n yaml.Node
		err := dec.Decode(&n)
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		if !emptyDocument(&n) {
			return errors.New("more than one YAML document (a seed file holds exactly one; split it into separate files)")
		}
	}
}

func emptyDocument(n *yaml.Node) bool {
	if n.Kind == yaml.DocumentNode && len(n.Content) == 1 {
		n = n.Content[0]
	}
	return n.Kind == 0 || (n.Kind == yaml.ScalarNode && n.Tag == "!!null")
}

// normalizeMap returns m with every nested map[any]any (produced by yaml.v3
// for non-string keys) converted to map[string]any so it marshals to JSON.
func normalizeMap(m map[string]any) map[string]any {
	if m == nil {
		return nil
	}
	out := make(map[string]any, len(m))
	for k, v := range m {
		out[k] = normalizeValue(v)
	}
	return out
}

func normalizeValue(v any) any {
	switch t := v.(type) {
	case map[string]any:
		return normalizeMap(t)
	case map[any]any:
		out := make(map[string]any, len(t))
		for k, val := range t {
			out[fmt.Sprint(k)] = normalizeValue(val)
		}
		return out
	case []any:
		out := make([]any, len(t))
		for i, val := range t {
			out[i] = normalizeValue(val)
		}
		return out
	}
	return v
}
