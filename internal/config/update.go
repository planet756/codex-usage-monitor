package config

import "errors"

// UpdateUI persists one immediate settings change without replacing data-source
// settings or unrelated edits made since the settings window was opened.
func UpdateUI(path, language, theme string) (Config, error) {
	_, updated, err := uiChange(path, language, theme)
	if err != nil {
		return updated, err
	}
	return updated, Save(path, updated)
}

// UpdateWindowsUI saves the controls and reconciles the startup registration
// as one change.
func UpdateWindowsUI(path, language, theme string, startup bool, apply func(bool) error) error {
	before, updated, err := uiChange(path, language, theme)
	if err != nil {
		return err
	}
	updated.StartWithWindows = startup
	return saveStartup(path, before, updated, apply)
}

func uiChange(path, language, theme string) (Config, Config, error) {
	c, err := Load(path)
	if err != nil {
		return c, c, err
	}
	if language != "en" && language != "zh-CN" || theme != "auto" && theme != "light" && theme != "dark" {
		return c, c, errors.New("invalid UI settings")
	}
	updated := c
	updated.Language, updated.Theme = language, theme
	return c, updated, nil
}

// UpdateStartup remembers an explicit choice before applying the registration.
// A failed write never changes registration; a failed registration restores the
// previous preference so the next launch cannot undo a reported failure.
func UpdateStartup(path string, enabled bool, apply func(bool) error) error {
	c, err := Load(path)
	if err != nil {
		return err
	}
	updated := c
	updated.StartWithWindows = enabled
	return saveStartup(path, c, updated, apply)
}

func saveStartup(path string, before, updated Config, apply func(bool) error) error {
	if before == updated {
		return apply(updated.StartWithWindows) // Still repair an absent or moved registration.
	}
	if err := Save(path, updated); err != nil {
		return err
	}
	if err := apply(updated.StartWithWindows); err != nil {
		return errors.Join(err, Save(path, before))
	}
	return nil
}
