package spawn

// DriverRepo is the repo whose hierarchy describes the DRIVER's own context: the
// repo containing the calling process's working directory, else home. A switch
// that a caller-chosen target repo must not be able to turn off (the key holder)
// reads it beside the target's.
func DriverRepo(home string) string { return driverRepo(home) }
