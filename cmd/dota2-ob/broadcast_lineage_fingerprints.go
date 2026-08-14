package main

// These fingerprints bind locally owned lineage artifacts to the exact source
// content compiled into the reviewed product. The companion test recomputes
// every digest from source, so changing implementation content without rotating
// the manifest identity fails the build instead of silently preserving a label.
// This constants-only file is deliberately excluded from the engine digest to
// avoid a self-referential hash.
const (
	contractsSourceSHA256         = "cb8513c20b816b681e083af043fb4ab13ea765b40b9dc055439e197c6644a8f9"
	liveMappingSourceSHA256       = "ea481ddba7f714d2f25248d696d0b4d65a8a23b3b901754f96180053fcb1b8a8"
	presentationCatalogSHA256     = "a4b5092c15110544092c532af928b59a08add2f80dfcdfb6b92f275af4fce11d"
	productMainSourceSHA256       = "e3692639044a02336923228ebefc8cdbced7c98e05157d6e2e06c2587c5c533f"
	productPortsSourceSHA256      = "34e4611547eb049b986cfe5a9709645df52cb4fb89c0a17d7973c13cabe0696f"
	productRecoverySourceSHA256   = "6b4797f0209ed843efef4d9bc51cbfb88a0c9b2fd5ddd943a679fdf3e21447b7"
	productRuntimeSourceSHA256    = "6c9c390d3e72596a95d7c0ae7189ed2bf77c1fbc93ddc5342d6bcfaa2f055410"
	productLineageSourceSHA256    = "101b80bbacfb33802a1bfa3c23d36e6a9da6f93c491455703390002efe62556c"
	productLiveOnlySourceSHA256   = "61ebd6da3217ed953c854fbae28197690845b088efa16743fd74b724ffe9ccfa"
	productRecoveryV3SourceSHA256 = "7417c1580323b2bb678d9415db4b5b6e62c1adfccb7bc8de62342956f45c4b30"
	productRuntimeV3SourceSHA256  = "811f55a7b63ae532a3738702ee664694bb9b1aa0aeefe04792e6f5efe29a3173"
	sessionHighWaterSourceSHA256  = "8cd02d084a5ae7077364e5c93872b2aa00a0f13ae0fbaa9e20d1abdf5a3263c7"
	sessionFollowerSourceSHA256   = "022ac146cdea343cd3c438eab8be585862874914d33fb16e765edbbdd2a8f435"
	insightEngineSourceSHA256     = "2c2e4d0ece8f89f3b2488d2db3109e4e1c2921ad2d165baf733855ecc9c6f08b"
	policyEngineSourceSHA256      = "fd1d0f801c3cc43f7dc367ac95083d335082b0bccdd42a35eedf7711c10029af"
	policyApplicationSourceSHA256 = "5678bc191e6e454694662488b4aa3af9950c1c14299aa70bc4babd3c84a6d771"
)
