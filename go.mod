module github.com/docker/docker-credential-helpers

go 1.21

retract (
	v0.9.1 // osxkeychain: a regression caused backward-incompatibility with earlier versions
	v0.9.0 // osxkeychain: a regression caused backward-incompatibility with earlier versions
)

require (
	github.com/1Password/connect-sdk-go v1.5.3
	github.com/danieljoos/wincred v1.2.3
	github.com/keybase/go-keychain v0.0.1
)

require (
	github.com/opentracing/opentracing-go v1.2.0 // indirect
	github.com/pkg/errors v0.9.1 // indirect
	github.com/uber/jaeger-client-go v2.30.0+incompatible // indirect
	github.com/uber/jaeger-lib v2.4.1+incompatible // indirect
	go.uber.org/atomic v1.9.0 // indirect
	golang.org/x/sys v0.20.0 // indirect
)

// Build module this module from my 1password fork/branch instead of upstream, this retains the official upstream docker-credential-helper module
replace github.com/docker/docker-credential-helpers => github.com/a-t-eight/docker-credential-helpers-1password v0.0.0-20251105135043-76b3860abcf6
