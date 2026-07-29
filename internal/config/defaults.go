package config

import "time"

func DefaultConfig() *Config {
	return &Config{
		Core: CoreConfig{
			Version: 1,
			Shell:   "",
			Mode:    "last",
			Debug:   false,
		},
		UI: UIConfig{
			Style:          "modern",
			GhostText:      true,
			MaxSuggestions: 100,
			MaxHeight:      15,
			NerdFonts:      true,
			Colors: ColorsConfig{
				Day: ColorPaletteConfig{
					Background:          "#f8f5f1",
					Border:              "#1d67f6",
					Accent:              "#3c9339",
					Muted:               "#747579",
					Text:                "#242529",
					TextSelected:        "#242529",
					Match:               "#084ccf",
					Description:         "#747579",
					DescriptionSelected: "#242529",
					SelectionBackground: "#dbe2f2",
					ScrollInfo:          "#984ea5",
					GhostText:           "#747579",
				},
				Night: ColorPaletteConfig{
					Background:          "#080a0d",
					Border:              "#739ee8",
					Accent:              "#61ffcf",
					Muted:               "#404658",
					Text:                "#c6cad7",
					TextSelected:        "#ffffff",
					Match:               "#61eeff",
					Description:         "#739ee8",
					DescriptionSelected: "#c6cad7",
					SelectionBackground: "#1a1e24",
					ScrollInfo:          "#fd7df4",
					GhostText:           "#404658",
				},
			},
		},
		Git: GitConfig{
			FilterActiveBranch:  true,
			DeduplicateBranches: true,
		},
		Updater: UpdaterConfig{
			CheckOnStartup: true,
			Channel:        "stable",
			CheckInterval:  Duration(24 * time.Hour),
		},
		AI: AIConfig{
			Enabled:       false,
			Provider:      "",
			DebounceMS:    500,
			MinIntervalMS: 1000,
			Providers:     nil,
			SuggestOnEmpty: SuggestOnEmptyConfig{
				Enabled:       false,
				DebounceMS:    800,
				MinIntervalMS: 5000,
			},
		},
	}
}

func DefaultState() *State {
	return &State{
		LastMode: "spec",
		Updater: UpdaterState{
			LastCheckTime: time.Time{},
			SeenVersion:   "",
		},
	}
}
