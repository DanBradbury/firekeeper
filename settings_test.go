package main

import "testing"

func TestPersistentSettingsRoundTrip(t *testing.T) {
	t.Setenv("FIREKEEPER_CONFIG_DIR", t.TempDir())

	settings, path, err := loadPersistentSettings()
	if err != nil {
		t.Fatal(err)
	}
	if settings.CodexSprite != codexSpriteRanger {
		t.Fatalf("default sprite = %s, want Ranger", settings.CodexSprite)
	}
	if err := savePersistentSettings(path, persistentSettings{CodexSprite: codexSpriteMage, CopilotSprite: codexSpriteWarrior, KimiSprite: codexSpriteRanger, Background: backgroundNone, BackgroundTime: backgroundNight}); err != nil {
		t.Fatal(err)
	}
	loaded, _, err := loadPersistentSettings()
	if err != nil {
		t.Fatal(err)
	}
	if loaded.CodexSprite != codexSpriteMage {
		t.Fatalf("loaded sprite = %s, want Mage", loaded.CodexSprite)
	}
	if loaded.CopilotSprite != codexSpriteWarrior || loaded.KimiSprite != codexSpriteRanger {
		t.Fatalf("loaded provider sprites = Copilot %s, Kimi %s", loaded.CopilotSprite, loaded.KimiSprite)
	}
	if loaded.Background != backgroundNone {
		t.Fatalf("loaded background = %s, want None", loaded.Background)
	}
	if loaded.BackgroundTime != backgroundNight {
		t.Fatalf("loaded background time = %s, want Night", loaded.BackgroundTime)
	}
}

func TestPersistentSettingsRejectInvalidSprite(t *testing.T) {
	t.Setenv("FIREKEEPER_CONFIG_DIR", t.TempDir())
	_, path, err := loadPersistentSettings()
	if err != nil {
		t.Fatal(err)
	}
	if err := savePersistentSettings(path, persistentSettings{CodexSprite: codexSpriteChoice(99)}); err != nil {
		t.Fatal(err)
	}
	settings, _, err := loadPersistentSettings()
	if err == nil {
		t.Fatal("invalid sprite setting accepted")
	}
	if settings.CodexSprite != codexSpriteRanger {
		t.Fatalf("invalid setting fallback = %s, want Ranger", settings.CodexSprite)
	}
}
