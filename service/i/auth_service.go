package i

import "github.com/sofc-t/puzzle-client/dmn"

type AuthServer interface {
	Login(username string, password string) (*dmn.Player, string, error)
	Register(username string, password string) error
}
