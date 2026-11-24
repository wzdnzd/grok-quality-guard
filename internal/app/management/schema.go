package management

import (
	"grok-quality-guard/internal/app/i18n"
	"grok-quality-guard/internal/config"
)

type settingsSchemaView struct {
	Groups []settingsGroupView `json:"groups"`
	Fields []config.FieldSpec  `json:"fields"`
}

type settingsGroupView struct {
	Key   string `json:"key"`
	Label string `json:"label"`
}

// settingsSchema returns the shared field specification the settings page
// renders bounds from. Validating in config and rendering here cannot drift,
// because both read the same FieldSpecs table.
func (s *Service) settingsSchema(locale string) settingsSchemaView {
	groups := make([]settingsGroupView, 0, len(config.FieldGroups))
	for _, group := range config.FieldGroups {
		groups = append(groups, settingsGroupView{Key: group.Key, Label: i18n.T(locale, group.Spec, nil)})
	}
	return settingsSchemaView{Groups: groups, Fields: config.FieldSpecs()}
}
