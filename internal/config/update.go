package config

import "errors"

// UpdateUI persists one immediate settings change without replacing data-source
// settings or unrelated edits made since the settings window was opened.
func UpdateUI(path, language, theme string) (Config, error) {
	c, err := Load(path)
	if err != nil {
		return c, err
	}
	if language != "en" && language != "zh-CN" || theme != "auto" && theme != "light" && theme != "dark" {
		return c, errors.New("invalid UI settings")
	}
	c.Language, c.Theme = language, theme
	return c, Save(path, c)
}

// UpdateStartup remembers an explicit choice before applying the registration.
// A failed write never changes registration; a failed registration restores the
// previous preference so the next launch cannot undo a reported failure.
func UpdateStartup(path string, enabled bool, apply func(bool) error) error {
	c, err := Load(path)
	if err != nil {
		return err
	}
	if c.StartWithWindows == enabled {
		return apply(enabled) // Still repair an absent or moved registration.
	}
	updated := c
	updated.StartWithWindows = enabled
	if err = Save(path, updated); err != nil {
		return err
	}
	if err = apply(enabled); err != nil {
		return errors.Join(err, Save(path, c))
	}
	return nil
}
