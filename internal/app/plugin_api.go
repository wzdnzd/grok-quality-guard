package app

// ABI entry points intentionally stay on Plugin: they are the only methods
// invoked by the C ABI and are the composition boundary for the domain
// services.
func (p *Plugin) RouteModel(request []byte) ([]byte, error) {
	return p.executorSvc.RouteModel(request)
}

func (p *Plugin) Execute(request []byte) ([]byte, error) {
	return p.executorSvc.Execute(request)
}

func (p *Plugin) ExecuteStream(request []byte) ([]byte, error) {
	return p.executorSvc.ExecuteStream(request)
}

func (p *Plugin) HandleManagement(request []byte) ([]byte, error) {
	return p.managementSvc.HandleManagement(request)
}

func (p *Plugin) InterceptResponse(request []byte) []byte {
	return p.passiveSvc.InterceptResponse(request)
}

func (p *Plugin) InterceptStreamChunk(request []byte) []byte {
	return p.passiveSvc.InterceptStreamChunk(request)
}

func (p *Plugin) HandleUsage(request []byte) {
	p.passiveSvc.HandleUsage(request)
}
