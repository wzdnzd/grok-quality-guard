package proxy

func CloneProxyState(source ProxyState) ProxyState {
	cloned := source
	cloned.Nodes = append([]Node(nil), source.Nodes...)
	cloned.Templates = make([]Template, len(source.Templates))
	for index, template := range source.Templates {
		cloned.Templates[index] = CloneTemplate(template)
	}
	cloned.AutoBind = cloneAutoBindConfig(source.AutoBind)
	cloned.NodeVersions = append([]Node(nil), source.NodeVersions...)
	cloned.TemplateVersions = make([]Template, len(source.TemplateVersions))
	for i, t := range source.TemplateVersions { cloned.TemplateVersions[i] = CloneTemplate(t) }
	if source.SourceRuntime != nil {
		cloned.SourceRuntime = make(map[string]SourceRuntime, len(source.SourceRuntime))
		for id, runtime := range source.SourceRuntime { cloned.SourceRuntime[id] = runtime }
	}
	if source.Operations != nil {
		cloned.Operations = make(map[string]Operation, len(source.Operations))
		for id, operation := range source.Operations { cloned.Operations[id] = CloneOperation(operation) }
	}
	cloned.Changes = make([]SourceChange, len(source.Changes))
	for index, change := range source.Changes {
		cloned.Changes[index] = change
		cloned.Changes[index].AuthIDs = append([]string(nil), change.AuthIDs...)
		cloned.Changes[index].Items = append([]ChangeItem(nil), change.Items...)
	}
	return cloned
}

func CloneOperation(operation Operation) Operation {
	cloned := operation
	cloned.Binding = CloneBinding(operation.Binding)
	if operation.OldLease != nil { lease := *operation.OldLease; cloned.OldLease = &lease }
	return cloned
}
func CloneTemplate(template Template) Template {
	cloned := template
	cloned.Placeholders = make([]Placeholder, len(template.Placeholders))
	for index, placeholder := range template.Placeholders {
		copied := placeholder
		if placeholder.Enum != nil {
			enumCopy := *placeholder.Enum
			enumCopy.Values = append([]string(nil), placeholder.Enum.Values...)
			copied.Enum = &enumCopy
		}
		if placeholder.Random != nil {
			randomCopy := *placeholder.Random
			copied.Random = &randomCopy
		}
		cloned.Placeholders[index] = copied
	}
	cloned.ValueGroups = make([]ValueGroup, len(template.ValueGroups))
	for index, group := range template.ValueGroups {
		copied := group
		copied.Columns = append([]string(nil), group.Columns...)
		copied.Rows = make([][]string, len(group.Rows))
		for rowIndex, row := range group.Rows {
			copied.Rows[rowIndex] = append([]string(nil), row...)
		}
		cloned.ValueGroups[index] = copied
	}
	return cloned
}
func CloneBinding(binding Binding) Binding {
	cloned := binding
	if binding.ResolvedValues != nil {
		cloned.ResolvedValues = make(map[string]string, len(binding.ResolvedValues))
		for key, value := range binding.ResolvedValues {
			cloned.ResolvedValues[key] = value
		}
	}
	if binding.Lease != nil {
		lease := *binding.Lease
		cloned.Lease = &lease
	}
	return cloned
}
func cloneAutoBindConfig(config AutoBindConfig) AutoBindConfig {
	cloned := config
	cloned.SourceIDs = append([]string(nil), config.SourceIDs...)
	return cloned
}
