# img-downloader
boring name for a complicated setup!

A standalone Go tool that mirrors MTG card and sealed product images (Scryfall
singles, TCGplayer sealed art) into a B2 bucket, bundling each set into a
deterministic zip and tracking fetch state so reruns only pull what changed.
Full usage docs land in a later task.
