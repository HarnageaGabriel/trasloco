package ast

// String returns the lower-case form name.
func (f TriggerForm) String() string {
	switch f {
	case TriggerNone:
		return "none"
	case TriggerList:
		return "list"
	case TriggerInsertion:
		return "insertion"
	default:
		return "full"
	}
}

// String returns the lower-case form name.
func (f ResourceTriggerForm) String() string {
	switch f {
	case ResourceTriggerNone:
		return "none"
	case ResourceTriggerTrue:
		return "true"
	case ResourceTriggerInsertion:
		return "insertion"
	default:
		return "full"
	}
}

// String returns the lower-case form name.
func (f ParametersForm) String() string {
	switch f {
	case ParametersMap:
		return "map"
	case ParametersInsertion:
		return "insertion"
	default:
		return "list"
	}
}

// String returns the lower-case form name.
func (f VariablesForm) String() string {
	switch f {
	case VariablesList:
		return "list"
	case VariablesInsertion:
		return "insertion"
	default:
		return "map"
	}
}

// String returns the lower-case kind name.
func (k AnyKind) String() string {
	switch k {
	case AnySeq:
		return "seq"
	case AnyMap:
		return "map"
	default:
		return "scalar"
	}
}

// String returns the script keyword.
func (k ScriptKind) String() string { return k.Keyword() }
