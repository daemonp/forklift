package config

type Config struct {
	V1Backend      string
	V2Backend      string
	Rules          []Rule
	SessionAffinity bool
}

type Rule struct {
	Path       string
	PathPrefix string
	Method     string
	Conditions []RuleCondition
	Backend    string
	Percentage float64
	Priority   int
	PathPrefixRewrite string
}

type RuleCondition struct {
	Type      string
	Parameter string
	Operator  string
	Value     string
	QueryParam string // New field for GET query parameters
}
