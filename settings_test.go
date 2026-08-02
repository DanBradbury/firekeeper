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
	if err := savePersistentSettings(path, persistentSettings{CodexSprite: codexSpriteCleric}); err != nil {
		t.Fatal(err)
	}
	loaded, _, err := loadPersistentSettings()
	if err != nil {
		t.Fatal(err)
	}
	if loaded.CodexSprite != codexSpriteCleric {
		t.Fatalf("loaded sprite = %s, want Cleric", loaded.CodexSprite)
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
