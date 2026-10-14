package config

// LoadRelaxed resolves config from defaults → env → flags and then validates.
// It is the single entry point for every read-only CLI command (check, status,
// serve), so "relaxed" means "does not start the daemon" — never "does not
// validate". operator-surface §5 step 6 requires `check` to print resolved
// config, which is only meaningful if that config is known to be well-formed;
// a malformed URL or repo string must fail `check`, not surface later.
func LoadRelaxed(fo *FlagOverrides) (Config, error) {
	c, err := fromEnvNoValidate()
	if err != nil {
		return Config{}, err
	}
	if err := ApplyFlags(&c, fo); err != nil {
		return Config{}, err
	}
	if err := Validate(c); err != nil {
		return Config{}, err
	}
	return c, nil
}
