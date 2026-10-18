// Connect splash strings: the server line plus playful status lines shown
// (one at a time, at random) while a login connects.
export const CONNECT_QUIP_COUNT = 56;

const english = [
    "Charging the hamsters",
    "Waking up the packets",
    "Polishing the X",
    "Untangling the cables",
    "Warming up the microphones",
    "Teaching bits to queue politely",
    "Feeding the server its morning coffee",
    "Counting packets twice, just in case",
    "Tuning the waveform",
    "Asking the router nicely",
    "Brushing the latency off",
    "Herding stray datagrams",
    "Inflating the bandwidth",
    "Greasing the sockets",
    "Lining up the voice bars",
    "Convincing the firewall we're friends",
    "Handing out encryption keys",
    "Whispering to the server",
    "Defrosting Arctic Night",
    "Spinning up the echo cancellers",
    "Rolling out the red carpet for your voice",
    "Shaking hands with TLS",
    "Sorting packets by colour",
    "Tightening the jitter buffer",
    "Calibrating the ping-o-meter",
    "Rounding up the codecs",
    "Checking under the server for monsters",
    "Folding the latency into a smaller shape",
    "Giving the hamsters a pep talk",
    "Making sure voice goes first",
    "Unrolling the channel tree",
    "Dusting off the speakers",
    "Persuading electrons to hurry up",
    "Plugging in the internet",
    "Tuning the ring to 440 Hz",
    "Negotiating with the NAT",
    "Building a bridge, packet by packet",
    "Charging the flux capacitor",
    "Telling the server you're coming",
    "Clearing the airwaves",
    "Stretching the packets before the run",
    "Loading friendly noises",
    "Checking that the mute button is not muted",
    "Reticulating the splines",
    "Laying the voice carpet",
    "Pre-heating the microphones",
    "Asking the packets to form a queue",
    "Turning it off and on again (just kidding)",
    "Counting to ten in binary",
    "Unboxing fresh packets",
    "Calming the nervous bits",
    "Sharpening the X",
    "Watering the server farm",
    "Tidying the channel list",
    "Synchronising the hamster wheels",
    "Almost there, the packets can see the server",
];

const german = [
    "Die Hamster werden aufgeladen",
    "Pakete werden geweckt",
    "Das X wird poliert",
    "Kabelsalat wird entwirrt",
    "Mikrofone werden vorgewärmt",
    "Bits lernen, sich höflich anzustellen",
    "Der Server bekommt seinen Morgenkaffee",
    "Pakete werden zur Sicherheit doppelt gezählt",
    "Die Wellenform wird gestimmt",
    "Der Router wird freundlich gefragt",
    "Die Latenz wird abgestaubt",
    "Verirrte Datagramme werden eingesammelt",
    "Die Bandbreite wird aufgepumpt",
    "Die Sockets werden geölt",
    "Die Sprachbalken stellen sich auf",
    "Die Firewall wird überzeugt, dass wir Freunde sind",
    "Schlüssel werden verteilt",
    "Dem Server wird etwas zugeflüstert",
    "Arctic Night wird aufgetaut",
    "Die Echounterdrückung läuft warm",
    "Für deine Stimme wird der rote Teppich ausgerollt",
    "TLS bekommt einen Handschlag",
    "Pakete werden nach Farben sortiert",
    "Der Jitter-Puffer wird nachgezogen",
    "Das Ping-o-Meter wird kalibriert",
    "Die Codecs werden zusammengetrommelt",
    "Unter dem Server wird nach Monstern gesucht",
    "Die Latenz wird kleiner gefaltet",
    "Die Hamster bekommen eine Motivationsrede",
    "Es wird sichergestellt, dass Sprache vorgeht",
    "Der Kanalbaum wird ausgerollt",
    "Die Lautsprecher werden entstaubt",
    "Elektronen werden zur Eile gebeten",
    "Das Internet wird eingesteckt",
    "Der Ring wird auf 440 Hz gestimmt",
    "Mit dem NAT wird verhandelt",
    "Eine Brücke wird gebaut, Paket für Paket",
    "Der Fluxkompensator wird geladen",
    "Der Server erfährt, dass du kommst",
    "Der Äther wird freigeräumt",
    "Die Pakete dehnen sich vor dem Lauf",
    "Freundliche Geräusche werden geladen",
    "Es wird geprüft, ob die Stummtaste nicht stumm ist",
    "Die Splines werden retikuliert",
    "Der Sprachteppich wird verlegt",
    "Die Mikrofone werden vorgeheizt",
    "Die Pakete stellen sich in eine Reihe",
    "Aus- und wieder einschalten (nur ein Scherz)",
    "Es wird binär bis zehn gezählt",
    "Frische Pakete werden ausgepackt",
    "Nervöse Bits werden beruhigt",
    "Das X wird geschärft",
    "Die Serverfarm wird gegossen",
    "Die Kanalliste wird aufgeräumt",
    "Die Hamsterräder werden synchronisiert",
    "Fast geschafft, die Pakete sehen den Server schon",
];

const quipKey = index => `connect.quip.${String(index + 1).padStart(2, "0")}`;
const catalog = lines => Object.fromEntries(lines.map((line, index) => [quipKey(index), `${line}…`]));

export const connectEnglish = {
    "connect.title": "Connecting to {host}",
    ...catalog(english),
};

export const connectGerman = {
    "connect.title": "Verbinde mit {host}",
    ...catalog(german),
};

export { quipKey };

// shuffledQuips returns every line index once in random order; the first line
// of a new round never repeats the last line of the previous one.
export function shuffledQuips(count = CONNECT_QUIP_COUNT, random = Math.random, avoid = -1) {
    const order = Array.from({ length: count }, (_, index) => index);
    for (let i = order.length - 1; i > 0; i--) {
        const j = Math.floor(random() * (i + 1));
        [order[i], order[j]] = [order[j], order[i]];
    }
    if (order.length > 1 && order[0] === avoid) [order[0], order[1]] = [order[1], order[0]];
    return order;
}

