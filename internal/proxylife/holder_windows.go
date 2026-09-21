package proxylife

// HolderOf reports that this device cannot say which process holds
// addr. Windows names the holder only through tools whose output is
// not a contract this build reads; the surfaces therefore print the
// command that answers it and leave the reading to the user, rather
// than parsing what may change under them.
func HolderOf(string) (Process, bool) { return Process{}, false }

// holderCommand is what a user runs to read the holder for themselves,
// since nothing here reads it for them.
func holderCommand(port string) string { return "netstat -ano | findstr " + port }

// describe reads nothing about a process. Where no holder is read at
// all, no pid was ever proven to be a proxy of this build's own.
func describe(int) Process { return Process{} }
