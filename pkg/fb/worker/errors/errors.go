package errors

import "net/http"

type UnauthenticatedError struct {

}

func(u UnauthenticatedError) Error() string {
	return ""
}

type AuthenticationFailedError struct {
	Request		*http.Request
	Response	*http.Response
}

func(u AuthenticationFailedError) Error() string {
	return "authentication failed"
}

type ParsingError struct {
	Stage		string
	Request		[]byte
	Response	[]byte
}

func(u ParsingError) Error() string {
	return u.Stage
}

type NoAlbumsError struct {
	Stage		string
	Request		[]byte
	Response	[]byte
}

func(u NoAlbumsError) Error() string {
	return u.Stage
}

type NoFriendsError struct {
	Stage		string
	Request		[]byte
	Response	[]byte
}

func(u NoFriendsError) Error() string {
	return u.Stage
}

type GenderUndefinedError struct {
	Stage		string
	Request		[]byte
	Response	[]byte
}

func(u GenderUndefinedError) Error() string {
	return u.Stage
}

type WorkerCheckpointError struct {
	Request		[]byte
	Response	[]byte
}

func(u WorkerCheckpointError) Error() string {
	return "account on checkpoint"
}


type BrokenLinkCheckpoint struct {
	Request		[]byte
	Response	[]byte
}

func(u BrokenLinkCheckpoint) Error() string {
	return "broken link checkpoint"
}