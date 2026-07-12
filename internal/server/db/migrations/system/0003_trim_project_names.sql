-- Normalize project names: stored trimmed (the store also trims on create
-- from now on), so name-sorted listings and name matching behave sanely.
UPDATE projects SET name = TRIM(name) WHERE name != TRIM(name);
