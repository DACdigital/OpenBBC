package types

import "sort"

type WizardField struct {
	Label    string `yaml:"label"`
	Type     string `yaml:"type"`
	Required bool   `yaml:"required"`
	Order    int    `yaml:"order"`
}

type WizardSchema struct {
	Version string                 `yaml:"version"`
	Wizard  map[string]WizardField `yaml:"wizard"`
}

type OrderedField struct {
	Key   string
	Field WizardField
}

func (s *WizardSchema) OrderedFields() []OrderedField {
	fields := make([]OrderedField, 0, len(s.Wizard))
	for k, v := range s.Wizard {
		fields = append(fields, OrderedField{Key: k, Field: v})
	}
	sort.Slice(fields, func(i, j int) bool {
		return fields[i].Field.Order < fields[j].Field.Order
	})
	return fields
}
