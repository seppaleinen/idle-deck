package config

import (
	"os"
	"reflect"
)

// LoadRelaxed resolves config from YAML → defaults → env → Keychain(secrets) → flags and then validates.
// It is the single entry point for every read-only CLI command (check, status,
// serve), so "relaxed" means "does not start the daemon" — never "does not
// validate". operator-surface §5 step 6 requires `check` to print resolved
// config, which is only meaningful if that config is known to be well-formed;
// a malformed URL or repo string must fail `check`, not surface later.
//
// YAML config is optional (D23, D34). If --config is provided, that file is used.
// Otherwise, the first existing file in DefaultConfigPaths() is used.
// Precedence: CLI flags > env vars > Keychain(secrets) > YAML > defaults.
func LoadRelaxed(fo *FlagOverrides) (Config, error) {
	// Step 1: Load YAML (if provided or found)
	var yamlCfg *Config
	var err error
	if fo != nil && fo.ConfigFile != nil && *fo.ConfigFile != "" {
		yamlCfg, err = LoadYAML(*fo.ConfigFile)
		if err != nil {
			return Config{}, err
		}
	} else {
		if path := FindConfigFile(); path != "" {
			yamlCfg, err = LoadYAML(path)
			if err != nil {
				return Config{}, err
			}
		}
	}

	// Step 2: Start with YAML base (or empty)
	c := Config{}
	if yamlCfg != nil {
		c = *yamlCfg
	}

	// Step 3: Apply defaults for fields not set by YAML
	c = applyDefaults(c)

	// Step 4: Override with env vars
	if err := applyEnv(&c); err != nil {
		return Config{}, err
	}

	// Step 5: Override with CLI flags
	if fo != nil {
		if err := ApplyFlags(&c, fo); err != nil {
			return Config{}, err
		}
	}

	// Step 6: Validate
	if err := Validate(c); err != nil {
		return Config{}, err
	}
	return c, nil
}

// applyDefaults returns a copy of cfg with default values for any fields still at zero values.
func applyDefaults(cfg Config) Config {
	root := reflect.ValueOf(&cfg).Elem()
	for _, f := range fields {
		field := root.FieldByName(f.name)
		if f.default_ != "" && isZero(field) {
			_ = setField(root, f, f.default_)
		}
	}
	return cfg
}

// isZero reports whether v is at its zero value.
func isZero(v reflect.Value) bool {
	switch v.Kind() {
	case reflect.String:
		return v.String() == ""
	case reflect.Int, reflect.Int64:
		return v.Int() == 0
	case reflect.Bool:
		return !v.Bool()
	default:
		return v.Interface() == reflect.Zero(v.Type()).Interface()
	}
}

// applyEnv applies environment variables to cfg, overriding existing values.
// For secret fields, it also checks the Keychain after env (precedence:
// env > Keychain > YAML/defaults). Keychain is only consulted when the
// env var is empty and no value was set by YAML.
func applyEnv(cfg *Config) error {
	root := reflect.ValueOf(cfg).Elem()
	for _, f := range fields {
		if val := os.Getenv(f.key); val != "" {
			if err := setField(root, f, val); err != nil {
				return err
			}
		}
	}
	// Resolve secrets from Keychain if env was not set and YAML didn't
	// provide a value (isZero checks the field after YAML + env).
	if Keychain != nil {
		if err := resolveSecrets(root); err != nil {
			return err
		}
	}
	return nil
}

// resolveSecrets checks the Keychain for secret fields that are still empty.
// Keychain is only consulted for fields marked secret and only after env vars
// have been applied. Precedence: env > Keychain > YAML/defaults.
func resolveSecrets(root reflect.Value) error {
	if Keychain == nil {
		return nil
	}
	for _, f := range fields {
		if !f.secret {
			continue
		}
		field := root.FieldByName(f.name)
		if !field.IsValid() || field.String() != "" {
			continue
		}
		// Find which keychain key maps to this field.
		var kcKey string
		switch f.name {
		case "GitHubToken":
			kcKey = KeyGitHubToken
		case "HarnessToken":
			kcKey = KeyHarnessToken
		default:
			continue
		}
		val, err := Keychain.Get(kcKey)
		if err == nil {
			_ = setField(root, f, val)
		}
		// Keychain misses are silently ignored — env, YAML, or defaults
		// will be used instead.
	}
	return nil
}
