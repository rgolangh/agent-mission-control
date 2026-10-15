module github.com/rgolangh/agent-mission-control

go 1.27

// Minimum toolchain for builds: 1.27.2 fixes net/http vulnerabilities
// (GO-2026-6603..6617) that 1.27.0-1.27.1 have. An older local go fetches it.
toolchain go1.27.2
