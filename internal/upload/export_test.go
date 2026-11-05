package upload

// Closing reports whether Close has been asked of u, which u's flushes
// read between batches; a test that stops a flush there waits on it.
func Closing(u *Uploader) bool { return u.closing.Load() }
