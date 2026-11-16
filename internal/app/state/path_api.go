package state

func ConfiguredPath(value string) string {
	return configuredStatePath(value)
}

func NormalizePath(value string) (string, error) {
	return normalizeStatePath(value)
}

func ReadPathPointer(bootstrapPath string) (string, error) {
	return readStatePathPointer(bootstrapPath)
}

func WritePathPointer(bootstrapPath, activePath string) error {
	return writeStatePathPointer(bootstrapPath, activePath)
}
