package config

func LoadRelaxed(fo *FlagOverrides) (Config, error) {
	c, err := fromEnvNoValidate()
	if err != nil {
		return Config{}, err
	}
	if err := ApplyFlags(&c, fo); err != nil {
		return Config{}, err
	}
	return c, nil
}