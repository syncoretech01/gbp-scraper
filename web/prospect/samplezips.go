package prospect

// SampleZIPAreas returns a bundled SAMPLE dataset of 60 well-known ZIP
// codes across ten large US cities (San Francisco, Los Angeles,
// New York, Chicago, Houston, Miami, Seattle, Austin, Denver, Boston).
//
// This is demo data, not a census product: centroids are rounded to
// 2-3 decimal places and populations are rough approximations, both
// good enough to try the query generator and centre a map, but not for
// production coverage decisions. Supply your own CSV via ParseZIPCSV
// for real coverage.
func SampleZIPAreas() []ZIPArea {
	return []ZIPArea{
		// San Francisco, CA
		{ZIP: "94102", City: "San Francisco", State: "CA", Latitude: 37.779, Longitude: -122.419, Population: 31000},
		{ZIP: "94103", City: "San Francisco", State: "CA", Latitude: 37.772, Longitude: -122.411, Population: 27000},
		{ZIP: "94110", City: "San Francisco", State: "CA", Latitude: 37.749, Longitude: -122.415, Population: 69000},
		{ZIP: "94117", City: "San Francisco", State: "CA", Latitude: 37.77, Longitude: -122.443, Population: 39000},
		{ZIP: "94121", City: "San Francisco", State: "CA", Latitude: 37.777, Longitude: -122.494, Population: 41000},
		{ZIP: "94133", City: "San Francisco", State: "CA", Latitude: 37.802, Longitude: -122.41, Population: 26000},
		// Los Angeles, CA
		{ZIP: "90001", City: "Los Angeles", State: "CA", Latitude: 33.972, Longitude: -118.248, Population: 57000},
		{ZIP: "90012", City: "Los Angeles", State: "CA", Latitude: 34.062, Longitude: -118.24, Population: 31000},
		{ZIP: "90026", City: "Los Angeles", State: "CA", Latitude: 34.079, Longitude: -118.264, Population: 68000},
		{ZIP: "90036", City: "Los Angeles", State: "CA", Latitude: 34.07, Longitude: -118.35, Population: 55000},
		{ZIP: "90045", City: "Los Angeles", State: "CA", Latitude: 33.953, Longitude: -118.4, Population: 41000},
		{ZIP: "90291", City: "Los Angeles", State: "CA", Latitude: 33.993, Longitude: -118.464, Population: 28000},
		// New York, NY
		{ZIP: "10001", City: "New York", State: "NY", Latitude: 40.751, Longitude: -73.997, Population: 21000},
		{ZIP: "10011", City: "New York", State: "NY", Latitude: 40.742, Longitude: -74.0, Population: 51000},
		{ZIP: "10016", City: "New York", State: "NY", Latitude: 40.745, Longitude: -73.978, Population: 54000},
		{ZIP: "10025", City: "New York", State: "NY", Latitude: 40.798, Longitude: -73.968, Population: 94000},
		{ZIP: "10036", City: "New York", State: "NY", Latitude: 40.759, Longitude: -73.99, Population: 28000},
		{ZIP: "10128", City: "New York", State: "NY", Latitude: 40.781, Longitude: -73.95, Population: 61000},
		// Chicago, IL
		{ZIP: "60611", City: "Chicago", State: "IL", Latitude: 41.895, Longitude: -87.62, Population: 33000},
		{ZIP: "60614", City: "Chicago", State: "IL", Latitude: 41.922, Longitude: -87.653, Population: 71000},
		{ZIP: "60622", City: "Chicago", State: "IL", Latitude: 41.902, Longitude: -87.683, Population: 54000},
		{ZIP: "60640", City: "Chicago", State: "IL", Latitude: 41.972, Longitude: -87.663, Population: 69000},
		{ZIP: "60647", City: "Chicago", State: "IL", Latitude: 41.921, Longitude: -87.702, Population: 87000},
		{ZIP: "60657", City: "Chicago", State: "IL", Latitude: 41.94, Longitude: -87.654, Population: 71000},
		// Houston, TX
		{ZIP: "77002", City: "Houston", State: "TX", Latitude: 29.756, Longitude: -95.365, Population: 12000},
		{ZIP: "77006", City: "Houston", State: "TX", Latitude: 29.741, Longitude: -95.391, Population: 22000},
		{ZIP: "77019", City: "Houston", State: "TX", Latitude: 29.752, Longitude: -95.41, Population: 22000},
		{ZIP: "77024", City: "Houston", State: "TX", Latitude: 29.77, Longitude: -95.52, Population: 36000},
		{ZIP: "77056", City: "Houston", State: "TX", Latitude: 29.747, Longitude: -95.467, Population: 19000},
		{ZIP: "77098", City: "Houston", State: "TX", Latitude: 29.735, Longitude: -95.415, Population: 14000},
		// Miami, FL
		{ZIP: "33125", City: "Miami", State: "FL", Latitude: 25.782, Longitude: -80.237, Population: 51000},
		{ZIP: "33130", City: "Miami", State: "FL", Latitude: 25.767, Longitude: -80.203, Population: 34000},
		{ZIP: "33132", City: "Miami", State: "FL", Latitude: 25.784, Longitude: -80.187, Population: 18000},
		{ZIP: "33133", City: "Miami", State: "FL", Latitude: 25.73, Longitude: -80.24, Population: 31000},
		{ZIP: "33137", City: "Miami", State: "FL", Latitude: 25.813, Longitude: -80.19, Population: 20000},
		{ZIP: "33145", City: "Miami", State: "FL", Latitude: 25.753, Longitude: -80.223, Population: 30000},
		// Seattle, WA
		{ZIP: "98101", City: "Seattle", State: "WA", Latitude: 47.611, Longitude: -122.334, Population: 13000},
		{ZIP: "98103", City: "Seattle", State: "WA", Latitude: 47.673, Longitude: -122.342, Population: 51000},
		{ZIP: "98105", City: "Seattle", State: "WA", Latitude: 47.663, Longitude: -122.301, Population: 47000},
		{ZIP: "98115", City: "Seattle", State: "WA", Latitude: 47.685, Longitude: -122.301, Population: 53000},
		{ZIP: "98122", City: "Seattle", State: "WA", Latitude: 47.611, Longitude: -122.304, Population: 39000},
		{ZIP: "98144", City: "Seattle", State: "WA", Latitude: 47.586, Longitude: -122.3, Population: 31000},
		// Austin, TX
		{ZIP: "78701", City: "Austin", State: "TX", Latitude: 30.271, Longitude: -97.743, Population: 12000},
		{ZIP: "78704", City: "Austin", State: "TX", Latitude: 30.245, Longitude: -97.766, Population: 47000},
		{ZIP: "78745", City: "Austin", State: "TX", Latitude: 30.207, Longitude: -97.796, Population: 60000},
		{ZIP: "78751", City: "Austin", State: "TX", Latitude: 30.31, Longitude: -97.723, Population: 15000},
		{ZIP: "78757", City: "Austin", State: "TX", Latitude: 30.351, Longitude: -97.732, Population: 22000},
		{ZIP: "78759", City: "Austin", State: "TX", Latitude: 30.402, Longitude: -97.755, Population: 42000},
		// Denver, CO
		{ZIP: "80202", City: "Denver", State: "CO", Latitude: 39.749, Longitude: -104.999, Population: 8000},
		{ZIP: "80203", City: "Denver", State: "CO", Latitude: 39.731, Longitude: -104.982, Population: 21000},
		{ZIP: "80205", City: "Denver", State: "CO", Latitude: 39.759, Longitude: -104.964, Population: 34000},
		{ZIP: "80206", City: "Denver", State: "CO", Latitude: 39.73, Longitude: -104.953, Population: 22000},
		{ZIP: "80211", City: "Denver", State: "CO", Latitude: 39.766, Longitude: -105.02, Population: 34000},
		{ZIP: "80220", City: "Denver", State: "CO", Latitude: 39.733, Longitude: -104.916, Population: 36000},
		// Boston, MA
		{ZIP: "02108", City: "Boston", State: "MA", Latitude: 42.357, Longitude: -71.065, Population: 4000},
		{ZIP: "02116", City: "Boston", State: "MA", Latitude: 42.35, Longitude: -71.076, Population: 22000},
		{ZIP: "02118", City: "Boston", State: "MA", Latitude: 42.338, Longitude: -71.07, Population: 27000},
		{ZIP: "02127", City: "Boston", State: "MA", Latitude: 42.335, Longitude: -71.045, Population: 35000},
		{ZIP: "02130", City: "Boston", State: "MA", Latitude: 42.309, Longitude: -71.114, Population: 38000},
		{ZIP: "02215", City: "Boston", State: "MA", Latitude: 42.347, Longitude: -71.103, Population: 24000},
	}
}
