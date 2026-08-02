package config

type Config struct {
	Server Server
	Unused string

	// OnlyAssigned is written by the consumer but never read: dead.
	OnlyAssigned string
	// OnlyLiteralKey appears as a composite-literal key in the consumer: dead.
	OnlyLiteralKey string
	// OnlyTestRead is read solely from a _test.go file in the consumer: dead.
	OnlyTestRead string
}

type Server struct {
	Addr string
}
