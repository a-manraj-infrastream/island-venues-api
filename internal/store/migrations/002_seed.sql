-- Seed venues. ON CONFLICT DO NOTHING keeps this idempotent and never
-- overwrites edits staff made after the first start-up.
INSERT INTO venues (id, name, town, capacity, price_per_day_mur, description, tags) VALUES
    ('port-louis-caudan-loft', 'Caudan Harbour Loft', 'Port Louis', 180, 95000, 'Industrial loft above the waterfront with views over the harbour and the Moka range.', ARRAY['waterfront', 'urban', 'conference']),
    ('grand-baie-lagoon-deck', 'Lagoon Deck', 'Grand Baie', 120, 78000, 'Open-air timber deck on the bay, ideal for sunset receptions.', ARRAY['beach', 'sunset', 'outdoor']),
    ('le-morne-brabant-pavilion', 'Brabant Pavilion', 'Le Morne', 250, 165000, 'Thatched pavilion at the foot of Le Morne Brabant facing the kitesurf lagoon.', ARRAY['beach', 'wedding', 'outdoor']),
    ('flic-en-flac-sunset-terrace', 'Sunset Terrace', 'Flic en Flac', 90, 54000, 'West-coast terrace among casuarina trees, a few steps from the sand.', ARRAY['beach', 'sunset']),
    ('mahebourg-old-boathouse', 'Old Boathouse', 'Mahébourg', 70, 42000, 'Restored stone boathouse on the Mahébourg waterfront overlooking Île aux Aigrettes.', ARRAY['heritage', 'waterfront']),
    ('belle-mare-dune-garden', 'Dune Garden', 'Belle Mare', 300, 185000, 'Landscaped garden behind the dunes of the east coast''s longest beach.', ARRAY['garden', 'wedding', 'outdoor']),
    ('trou-aux-biches-reef-house', 'Reef House', 'Trou aux Biches', 60, 48000, 'Intimate beach house with a reef-facing veranda for small gatherings.', ARRAY['beach', 'intimate']),
    ('curepipe-colonial-hall', 'Colonial Hall', 'Curepipe', 220, 88000, 'High-ceilinged colonial hall on the cool central plateau, rain or shine.', ARRAY['heritage', 'indoor', 'conference']),
    ('pamplemousses-botanic-glasshouse', 'Botanic Glasshouse', 'Pamplemousses', 100, 72000, 'Glasshouse beside the giant water lilies, surrounded by palms and spice trees.', ARRAY['garden', 'heritage']),
    ('tamarin-salt-pans-studio', 'Salt Pans Studio', 'Tamarin', 50, 36000, 'Minimal studio next to the old salt pans, popular for workshops and launches.', ARRAY['workshop', 'indoor']),
    ('chamarel-highland-lodge', 'Highland Lodge', 'Chamarel', 80, 61000, 'Hillside lodge above the seven coloured earths with forest views.', ARRAY['mountain', 'retreat']),
    ('blue-bay-marine-park-cove', 'Marine Park Cove', 'Blue Bay', 140, 99000, 'Sheltered cove next to the marine park with turquoise shallows.', ARRAY['beach', 'wedding'])
ON CONFLICT (id) DO NOTHING;
