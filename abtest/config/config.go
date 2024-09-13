package config

type Config struct {
	V1Backend string
	V2Backend string
	Rules     []Rule
}

type Rule struct {
	Path       string
	Method     string
	Conditions []RuleCondition
	Backend    string
	Percentage float64
}

type RuleCondition struct {
	Type      string
	Parameter string
	Operator  string
	Value     string
}
