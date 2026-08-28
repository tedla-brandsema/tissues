module github.com/tedla-brandsema/tissues/lib/server

go 1.25.3

require github.com/tedla-brandsema/tissues/lib/core v0.0.0

require (
	github.com/tedla-brandsema/tagex v0.5.0 // indirect
	github.com/tedla-brandsema/valex v0.3.0 // indirect
	go.yaml.in/yaml/v3 v3.0.5 // indirect
)

replace github.com/tedla-brandsema/tissues/lib/core => ../core
