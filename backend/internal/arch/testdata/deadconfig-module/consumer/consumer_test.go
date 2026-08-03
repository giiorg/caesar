package consumer

import "example.com/deadconfig/config"

// testRead reads OnlyTestRead — but from a test file, so it must not count.
func testRead(c config.Config) string { return c.OnlyTestRead }
