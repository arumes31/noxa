-- Role images are separate from legacy text/emoji icons.
ALTER TABLE auth_roles ADD COLUMN icon_image TEXT NOT NULL DEFAULT '' CHECK (octet_length(icon_image) <= 10946);
