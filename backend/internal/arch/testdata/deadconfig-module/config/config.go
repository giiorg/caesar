package config

type Config struct {
	Server Server
	Unused string
}

type Server struct {
	Addr string
}
