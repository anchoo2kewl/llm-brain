package agent

import (
	"bufio"
	"bytes"
	"context"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Config is the little of the fleet config the executor needs, read the way bin/lib.sh layers it:
// config/fleet.yaml, then the overlay's config.yaml, then ~/.config/llm-brain/local.yaml (later wins).
type Config struct {
	WorkRoot       string
	GatewayEnabled bool
	GatewayURL     string
	GatewayEnv     string // file holding CPA_API_KEY
}

func LoadConfig(brainDir string) Config {
	home, _ := os.UserHomeDir()
	local := filepath.Join(home, ".config/llm-brain/local.yaml")
	kv := map[string]string{}
	merge := func(p string) {
		if b, err := os.ReadFile(p); err == nil {
			for k, v := range yamlScalars(b) {
				kv[k] = v
			}
		}
	}
	if brainDir != "" {
		merge(filepath.Join(brainDir, "config/fleet.yaml"))
	}
	ov := os.Getenv("BRAIN_OVERLAY")
	if ov == "" {
		if b, err := os.ReadFile(local); err == nil {
			ov = yamlScalars(b)["overlay"]
		}
	}
	if ov == "" {
		ov = filepath.Join(home, "projects/llm-brain-overlay")
	}
	merge(filepath.Join(expandHome(ov), "config.yaml"))
	merge(local)
	c := Config{
		WorkRoot:       expandHome(kv["work_root"]),
		GatewayEnabled: kv["gateway.enabled"] == "true",
		GatewayURL:     strings.TrimRight(kv["gateway.url"], "/"),
		GatewayEnv:     expandHome(kv["gateway.env_file"]),
	}
	if c.GatewayEnv == "" {
		c.GatewayEnv = filepath.Join(home, ".config/llm-brain/cpa.env")
	}
	if v := os.Getenv("FLEET_WORK_ROOT"); v != "" {
		c.WorkRoot = expandHome(v)
	}
	return c
}

func expandHome(p string) string {
	if p == "~" || strings.HasPrefix(p, "~/") {
		home, _ := os.UserHomeDir()
		return filepath.Join(home, strings.TrimPrefix(p, "~"))
	}
	return p
}

// yamlScalars reads `key: value` at the top level and one level down ("gateway.url"). Enough for the keys
// above; lists and deeper maps are skipped.
func yamlScalars(b []byte) map[string]string {
	out := map[string]string{}
	section := ""
	sc := bufio.NewScanner(bytes.NewReader(b))
	for sc.Scan() {
		raw := sc.Text()
		t := strings.TrimSpace(raw)
		if t == "" || strings.HasPrefix(t, "#") || strings.HasPrefix(t, "-") {
			continue
		}
		indent := len(raw) - len(strings.TrimLeft(raw, " \t"))
		k, v, ok := strings.Cut(t, ":")
		if !ok {
			continue
		}
		k = strings.TrimSpace(k)
		v = strings.TrimSpace(v)
		if i := strings.Index(v, " #"); i >= 0 {
			v = strings.TrimSpace(v[:i])
		}
		if strings.HasPrefix(v, "#") {
			v = ""
		}
		v = strings.Trim(v, `"'`)
		if indent == 0 {
			section = ""
			if v == "" {
				section = k
				continue
			}
			out[k] = v
		} else if section != "" && v != "" {
			out[section+"."+k] = v
		}
	}
	return out
}

// envValue reads KEY=VALUE (optionally `export KEY=VALUE`) from an env file.
func envValue(path, key string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	sc := bufio.NewScanner(bytes.NewReader(b))
	for sc.Scan() {
		t := strings.TrimPrefix(strings.TrimSpace(sc.Text()), "export ")
		if k, v, ok := strings.Cut(t, "="); ok && strings.TrimSpace(k) == key {
			return strings.Trim(strings.TrimSpace(v), `"'`)
		}
	}
	return ""
}

// ResolveGateway is the gateway to use now: enabled in the config, a CPA_API_KEY in its env file, and
// answering /v1/models. Otherwise the CLI uses its own login (zero Gateway).
func (c Config) ResolveGateway(ctx context.Context) Gateway {
	if !c.GatewayEnabled || c.GatewayURL == "" {
		return Gateway{}
	}
	key := envValue(c.GatewayEnv, "CPA_API_KEY")
	if key == "" {
		return Gateway{}
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.GatewayURL+"/v1/models", nil)
	if err != nil {
		return Gateway{}
	}
	req.Header.Set("Authorization", "Bearer "+key)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return Gateway{}
	}
	resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return Gateway{}
	}
	return Gateway{URL: c.GatewayURL, Key: key}
}
