package store

// SeedVenues returns the twelve fictional venues every new store starts with.
// The same rows live in migrations/002_seed.sql; TestSeedMatchesMigration
// fails if the two drift apart.
func SeedVenues() []Venue {
	return []Venue{
		{ID: "port-louis-caudan-loft", Name: "Caudan Harbour Loft", Town: "Port Louis", Capacity: 180, PricePerDayMur: 95000, Description: "Industrial loft above the waterfront with views over the harbour and the Moka range.", Tags: []string{"waterfront", "urban", "conference"}},
		{ID: "grand-baie-lagoon-deck", Name: "Lagoon Deck", Town: "Grand Baie", Capacity: 120, PricePerDayMur: 78000, Description: "Open-air timber deck on the bay, ideal for sunset receptions.", Tags: []string{"beach", "sunset", "outdoor"}},
		{ID: "le-morne-brabant-pavilion", Name: "Brabant Pavilion", Town: "Le Morne", Capacity: 250, PricePerDayMur: 165000, Description: "Thatched pavilion at the foot of Le Morne Brabant facing the kitesurf lagoon.", Tags: []string{"beach", "wedding", "outdoor"}},
		{ID: "flic-en-flac-sunset-terrace", Name: "Sunset Terrace", Town: "Flic en Flac", Capacity: 90, PricePerDayMur: 54000, Description: "West-coast terrace among casuarina trees, a few steps from the sand.", Tags: []string{"beach", "sunset"}},
		{ID: "mahebourg-old-boathouse", Name: "Old Boathouse", Town: "Mahébourg", Capacity: 70, PricePerDayMur: 42000, Description: "Restored stone boathouse on the Mahébourg waterfront overlooking Île aux Aigrettes.", Tags: []string{"heritage", "waterfront"}},
		{ID: "belle-mare-dune-garden", Name: "Dune Garden", Town: "Belle Mare", Capacity: 300, PricePerDayMur: 185000, Description: "Landscaped garden behind the dunes of the east coast's longest beach.", Tags: []string{"garden", "wedding", "outdoor"}},
		{ID: "trou-aux-biches-reef-house", Name: "Reef House", Town: "Trou aux Biches", Capacity: 60, PricePerDayMur: 48000, Description: "Intimate beach house with a reef-facing veranda for small gatherings.", Tags: []string{"beach", "intimate"}},
		{ID: "curepipe-colonial-hall", Name: "Colonial Hall", Town: "Curepipe", Capacity: 220, PricePerDayMur: 88000, Description: "High-ceilinged colonial hall on the cool central plateau, rain or shine.", Tags: []string{"heritage", "indoor", "conference"}},
		{ID: "pamplemousses-botanic-glasshouse", Name: "Botanic Glasshouse", Town: "Pamplemousses", Capacity: 100, PricePerDayMur: 72000, Description: "Glasshouse beside the giant water lilies, surrounded by palms and spice trees.", Tags: []string{"garden", "heritage"}},
		{ID: "tamarin-salt-pans-studio", Name: "Salt Pans Studio", Town: "Tamarin", Capacity: 50, PricePerDayMur: 36000, Description: "Minimal studio next to the old salt pans, popular for workshops and launches.", Tags: []string{"workshop", "indoor"}},
		{ID: "chamarel-highland-lodge", Name: "Highland Lodge", Town: "Chamarel", Capacity: 80, PricePerDayMur: 61000, Description: "Hillside lodge above the seven coloured earths with forest views.", Tags: []string{"mountain", "retreat"}},
		{ID: "blue-bay-marine-park-cove", Name: "Marine Park Cove", Town: "Blue Bay", Capacity: 140, PricePerDayMur: 99000, Description: "Sheltered cove next to the marine park with turquoise shallows.", Tags: []string{"beach", "wedding"}},
	}
}
