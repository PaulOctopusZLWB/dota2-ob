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
	productSelectorSourceSHA256   = "83e7016996cfcee081fe85bc7c2f6628b7257b100b1aa14079778d3cd55456a0"
	presentationCatalogSHA256     = "a4b5092c15110544092c532af928b59a08add2f80dfcdfb6b92f275af4fce11d"
	productMainSourceSHA256       = "2604f5422188b697591666201c70c8a41729ea6418755d2c2a2847aa451adae9"
	productPortsSourceSHA256      = "d86ba36f415cac9bac7e5bb2e8028c9f7bdd58a5f7bd0d819625d26abb1aef97"
	productRecoverySourceSHA256   = "ce8c0d14fadf78973cfea56f577dfb67da0aac43a85d729498065ffaace6b31d"
	productRuntimeSourceSHA256    = "4d84be98a0371b49a2fc7e010cdda7bd89f94b482873bf129fdde7f9b727a7ec"
	productLineageSourceSHA256    = "4177fd172df569cee7b20d22540f5e15c7ac1fd341bbfb54978ae16ac7952ea5"
	productLiveOnlySourceSHA256   = "61ebd6da3217ed953c854fbae28197690845b088efa16743fd74b724ffe9ccfa"
	productRecoveryV3SourceSHA256 = "0b0603b383916abf6cb1e1e56943de872729880ca115d59e9179bda4f5592c66"
	productRuntimeV3SourceSHA256  = "1c48565d32a7cd3d72fea43b26fdca1dc23ab11a128b23b1dbdaa1b282ecae97"
	sessionHighWaterSourceSHA256  = "8cd02d084a5ae7077364e5c93872b2aa00a0f13ae0fbaa9e20d1abdf5a3263c7"
	sessionFollowerSourceSHA256   = "022ac146cdea343cd3c438eab8be585862874914d33fb16e765edbbdd2a8f435"
	insightEngineSourceSHA256     = "2c2e4d0ece8f89f3b2488d2db3109e4e1c2921ad2d165baf733855ecc9c6f08b"
	insightLiveOnlyV2SourceSHA256 = "6a217189f08a81e73d67093c1c4659b11ceacb6ce82495f20b079ee0c26b3488"
	policyEngineSourceSHA256      = "fd1d0f801c3cc43f7dc367ac95083d335082b0bccdd42a35eedf7711c10029af"
	policyApplicationSourceSHA256 = "5678bc191e6e454694662488b4aa3af9950c1c14299aa70bc4babd3c84a6d771"
)
