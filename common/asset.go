package common

// AssetLibraryEnabled is fixed at startup. All nodes sharing a database must
// enable it together: durable task reservations require database-authoritative
// wallet and token balances, including requests that do not reference assets.
var AssetLibraryEnabled = GetEnvOrDefaultBool("ASSET_LIBRARY_ENABLED", false)
