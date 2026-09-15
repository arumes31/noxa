# noXa rename

The repository, Go modules, executable names, release assets and URL scheme use
`noxa`. The product name shown in the app is **noXa**. The repository is
<https://github.com/arumes31/noxa>.

## Existing clients

On first startup, the desktop client moves `<UserConfigDir>/voicx` to
`<UserConfigDir>/noxa` before opening settings, identities, trust pins or logs.
If a `noxa` profile already exists, it is used and the old profile is left alone.
A failed migration stops startup rather than creating a replacement identity.

Cryptographic domain labels, the encrypted-export magic bytes, historical SQL
migrations and the migration lock ID retain their original values. These are
data-format identifiers, not branding: changing them would invalidate encrypted
data or migration checksums. New encrypted exports use `.noxachat`.

## Existing servers

Rename configuration variables from `VOICX_*` to `NOXA_*`. Keep existing database
credentials, database names, encryption keys and data paths as their values.
The local `.env` keys are updated by this rename; deployed environments must be
updated separately. Upgrade gRPC consumers together with the server: generated
services now use the `noxa.v1` namespace. New invite links use `noxa://`.

Fresh Docker deployments use `noxa-*` volume names. To reuse the previous
deployment's volumes, put these overrides in its `.env` before starting Compose:

```dotenv
NOXA_PGDATA_VOLUME=voicx-pgdata
NOXA_PGBACKUPS_VOLUME=voicx-pgbackups
NOXA_REDISDATA_VOLUME=voicx-redisdata
NOXA_DATA_VOLUME=voicx-data
```

Keep the existing PostgreSQL user, password and database name in `POSTGRES_*`
and the database URL. Stop the old stack before starting the renamed services;
the rename does not deploy or move running containers.

Release workflows read `NOXA_UPDATE_PUBLIC_KEYS` and `NOXA_UPDATE_SIGNING_KEY`,
falling back to the existing `VOICX_*` repository variable and secret. Existing
published assets retain their old names; future releases produce `noxa-*` assets.
