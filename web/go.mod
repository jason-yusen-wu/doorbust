// Not a Go project — this file exists solely to stop the root module's `./...`
// from descending into web/.
//
// npm dependencies occasionally ship Go source (flatted vendors a package under
// node_modules/flatted/golang), and with no module boundary here the go tool
// absorbs it into github.com/jason-yusen-wu/doorbust. That puts third-party
// code into `go vet ./...`, `go test ./...` and the `-coverpkg=./...`
// denominator. A module boundary is the documented way to stop that; nothing
// ever builds or requires this module.
module github.com/jason-yusen-wu/doorbust/web

go 1.26
