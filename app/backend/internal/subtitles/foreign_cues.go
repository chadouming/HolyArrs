package subtitles

import (
	"context"
	"log"
	"regexp"
	"slices"
	"strings"
	"unicode"

	"github.com/HolyArrs/holyarrs/app/backend/internal/subtitles/subtext"
	"github.com/HolyArrs/holyarrs/app/backend/internal/subtitles/translate"
)

var cueTagRe = regexp.MustCompile(`<[^>]{1,24}>`)

// latinTargetLangs are target languages written in the Latin alphabet.
var latinTargetLangs = map[string]bool{
	"en": true, "es": true, "fr": true, "de": true, "pt": true,
	"it": true, "nl": true, "pl": true, "tr": true, "sv": true,
	"da": true, "no": true, "nb": true, "nn": true, "fi": true,
	"cs": true, "sk": true, "ro": true, "hu": true, "hr": true,
	"sl": true, "id": true, "ms": true, "vi": true, "ca": true,
	"gl": true, "eu": true, "tl": true, "sw": true,
}

// nonLatinTargetLangs are target languages written in non-Latin scripts.
var nonLatinTargetLangs = map[string]bool{
	"ru": true, "uk": true, "bg": true, "ja": true, "zh": true,
	"ko": true, "ar": true, "fa": true, "ur": true, "he": true,
	"el": true, "th": true, "hi": true,
}

// scoredLatinLangs sets the deterministic evaluation order for Latin-script
// language scoring.
var scoredLatinLangs = []string{
	"es", "fr", "de", "pt", "it", "nl", "pl", "tr", "sv", "ro", "en",
}

// latinStopwords holds closed-class function words per language. Distinctive
// words carry weight 2; shared or ambiguous words carry weight 1.
var latinStopwords = map[string]map[string]int{
	"en": {
		"the": 2, "and": 2, "that": 2, "have": 2, "for": 2, "not": 2, "with": 2,
		"you": 2, "this": 2, "but": 2, "his": 2, "from": 2, "they": 2, "she": 2,
		"which": 2, "will": 2, "would": 2, "there": 2, "their": 2, "what": 2,
		"about": 2, "when": 2, "can": 2, "can't": 2, "don't": 2, "didn't": 2,
		"won't": 2, "isn't": 2, "aren't": 2, "wasn't": 2, "weren't": 2,
		"couldn't": 2, "wouldn't": 2, "shouldn't": 2, "doesn't": 2,
		"hasn't": 2, "haven't": 2, "hadn't": 2, "ain't": 2, "i'm": 2,
		"you're": 2, "we're": 2, "they're": 2, "it's": 2, "that's": 2,
		"what's": 2, "who's": 2, "there's": 2, "here's": 2, "he's": 2,
		"she's": 2, "let's": 2, "i've": 2, "you've": 2, "i'll": 2, "you'll": 2,
		"we'll": 2, "they'll": 2, "i'd": 2, "you'd": 2, "we'd": 2,
		"they'd": 2, "were": 2, "been": 2, "has": 2, "had": 2, "are": 2,
		"was": 2, "your": 2, "our": 2, "my": 2, "her": 2, "him": 2, "them": 2,
		"some": 2, "could": 2, "into": 2, "than": 2, "then": 2, "these": 2,
		"those": 2, "where": 2, "why": 2, "who": 2, "how": 2, "he": 2, "it": 2,
		"is": 2, "in": 2, "to": 2, "of": 2, "we": 2, "be": 2, "if": 2, "as": 2,
		"at": 2, "by": 2, "or": 2, "an": 2, "i": 2, "because": 2, "should": 2,
		"am": 2, "one": 2, "go": 2, "come": 2, "want": 2, "said": 2, "got": 2,
		"make": 2, "made": 2, "know": 2, "think": 2, "take": 2, "time": 2,
		"now": 2, "here": 2, "out": 2, "up": 2, "down": 2, "off": 2, "back": 2,
		"all": 2, "very": 2, "much": 2, "really": 2, "gonna": 2, "wanna": 2,
		"gotta": 2,
		"a": 1, "on": 1, "no": 1, "me": 1, "do": 1, "so": 1, "yes": 1,
		"yeah": 1, "ok": 1, "okay": 1, "just": 1, "like": 1, "son": 1,
		"die": 1, "hat": 1, "war": 1, "met": 1, "care": 1, "mine": 1, "men": 1,
		"car": 1, "bin": 1, "dig": 1, "heel": 1, "hem": 1, "per": 1, "oh": 1,
		"hey": 1, "hi": 1, "bye": 1, "wait": 1, "stop": 1, "look": 1, "see": 1,
		"say": 1, "get": 1,
	},
	"es": {
		"una": 2, "unos": 2, "unas": 2, "yo": 2, "tú": 2, "él": 2, "ella": 2,
		"ellos": 2, "ellas": 2, "nosotros": 2, "ustedes": 2, "eso": 2,
		"esto": 2, "esta": 2, "este": 2, "estos": 2, "estas": 2, "ese": 2,
		"esa": 2, "esos": 2, "esas": 2, "aquel": 2, "aquella": 2, "pero": 2,
		"porque": 2, "cómo": 2, "qué": 2, "quién": 2, "cuál": 2, "cuándo": 2,
		"dónde": 2, "porqué": 2, "con": 1, "sobre": 2, "entre": 1, "hasta": 2,
		"desde": 2, "hacia": 2, "muy": 2, "más": 2, "menos": 1, "también": 2,
		"tampoco": 2, "siempre": 2, "nunca": 2, "nada": 2, "nadie": 2,
		"algo": 2, "alguien": 2, "todo": 2, "todos": 1, "toda": 2, "todas": 2,
		"otro": 2, "otra": 2, "otros": 2, "otras": 2, "mismo": 2, "misma": 2,
		"ahora": 2, "aquí": 2, "allí": 2, "ahí": 2, "entonces": 2, "luego": 2,
		"después": 2, "antes": 2, "hoy": 2, "ayer": 2, "mañana": 2, "sí": 2,
		"eres": 2, "es": 2, "somos": 2, "estoy": 2, "estás": 2, "está": 2,
		"estamos": 2, "están": 2, "fue": 2, "fueron": 2, "ser": 2, "estar": 2,
		"tengo": 2, "tienes": 2, "tiene": 2, "tenemos": 2, "tienen": 2,
		"tener": 2, "hacer": 2, "hago": 2, "hace": 2, "hacen": 2, "quiero": 2,
		"quieres": 2, "quiere": 2, "puedo": 2, "puedes": 2, "puede": 2,
		"podemos": 2, "pueden": 2, "voy": 2, "vamos": 2, "había": 2,
		"hola": 2, "gracias": 2, "perdón": 2, "bueno": 2, "buena": 2,
		"mucho": 2, "mucha": 2, "muchos": 2, "muchas": 2, "poco": 2, "poca": 2,
		"bien": 2, "claro": 2, "verdad": 2, "cierto": 2, "creo": 2,
		"siento": 2, "digo": 2, "dice": 2, "dije": 2, "sabes": 2, "sabe": 2,
		"sé": 2, "mira": 2, "oye": 2, "ven": 2, "dame": 2, "venga": 2, "y": 2,
		"dale": 1, "el": 1, "los": 1, "las": 1, "del": 1, "al": 1, "un": 1,
		"como": 1, "que": 1, "para": 1, "por": 1, "sin": 1, "son": 1, "soy": 1,
		"vas": 1, "va": 1, "van": 1, "hay": 1, "mal": 1, "me": 1, "te": 1,
		"se": 1, "mi": 1, "tu": 1, "su": 1, "ti": 1, "si": 1, "nos": 1,
		"lo": 1, "la": 1, "le": 1, "les": 1, "de": 1, "en": 1, "a": 1, "o": 1,
		"ni": 1, "no": 1, "ya": 1, "tan": 1, "tal": 1, "era": 1,
	},
	"fr": {
		"cette": 2, "nous": 2, "vous": 2, "ils": 2, "elles": 2, "je": 2,
		"lui": 2, "leur": 2, "quoi": 2, "où": 2, "jamais": 2, "rien": 2,
		"tout": 2, "tous": 2, "toute": 2, "très": 2, "mais": 2, "donc": 2,
		"parce": 2, "quand": 2, "comme": 2, "avec": 2, "sans": 2, "dans": 2,
		"sous": 2, "chez": 2, "vers": 2, "suis": 2, "sommes": 2, "êtes": 2,
		"sont": 2, "étais": 2, "était": 2, "être": 2, "avons": 2, "avez": 2,
		"ont": 2, "avoir": 2, "fais": 2, "fait": 2, "faire": 2, "vais": 2,
		"allons": 2, "allez": 2, "vont": 2, "oui": 2, "merci": 2,
		"bonjour": 2, "salut": 2, "voilà": 2, "c'est": 2, "j'ai": 2,
		"n'est": 2, "qu'il": 2, "qu'on": 2, "d'un": 2, "d'une": 2, "l'on": 2,
		"c'était": 2, "aujourd'hui": 2, "peux": 2, "peut": 2, "veux": 2,
		"veut": 2, "sais": 2, "sait": 2, "alors": 2, "aussi": 2, "encore": 2,
		"toujours": 2, "maintenant": 2, "ici": 2, "peu": 2, "beaucoup": 2,
		"trop": 2,
		"de": 1, "le": 1, "la": 1, "les": 1, "un": 1, "une": 1, "et": 1,
		"est": 1, "en": 1, "du": 1, "des": 1, "au": 1, "aux": 1, "ce": 1,
		"ces": 1, "cet": 1, "il": 1, "elle": 1, "on": 1, "tu": 1, "me": 1,
		"te": 1, "se": 1, "mon": 1, "ma": 1, "mes": 1, "ton": 1, "ta": 1,
		"tes": 1, "son": 1, "sa": 1, "ses": 1, "qui": 1, "que": 1, "dont": 1,
		"ne": 1, "pas": 1, "plus": 1, "bien": 1, "mal": 1, "ou": 1, "car": 1,
		"ni": 1, "si": 1, "pour": 1, "sur": 1, "entre": 1, "ai": 1, "as": 1,
		"a": 1, "vas": 1, "va": 1, "non": 1, "pardon": 1, "là": 1,
	},
	"de": {
		"das": 2, "ein": 2, "eine": 2, "einen": 2, "einem": 2, "einer": 2,
		"und": 2, "ich": 2, "sie": 2, "wir": 2, "ihr": 2, "mich": 2,
		"mir": 2, "dich": 2, "dir": 2, "sich": 2, "uns": 2, "euch": 2,
		"mein": 2, "meine": 2, "dein": 2, "deine": 2, "sein": 2, "seine": 2,
		"ist": 2, "bist": 2, "sind": 2, "seid": 2, "waren": 2,
		"habe": 2, "hast": 2, "haben": 2, "kann": 2, "muss": 2, "soll": 2,
		"nicht": 2, "kein": 2, "keine": 2, "nein": 2, "auch": 2, "noch": 2,
		"schon": 2, "aber": 2, "oder": 2, "wenn": 2, "weil": 2, "dass": 2,
		"wie": 2, "wer": 2, "warum": 2, "wann": 2, "sehr": 2, "hier": 2,
		"jetzt": 2, "immer": 2, "alles": 2, "nichts": 2, "etwas": 2, "mit": 2,
		"aus": 2, "auf": 2, "für": 2, "über": 2, "unter": 2, "nach": 2,
		"vor": 2, "bei": 2, "seit": 2, "durch": 2, "gegen": 2, "ohne": 2,
		"zu": 2, "zum": 2, "zur": 2, "vom": 2, "doch": 2, "nur": 2,
		"bitte": 2, "danke": 2, "hallo": 2, "guten": 2,
		"bin": 1, "der": 1, "die": 1, "den": 1, "dem": 1, "des": 1, "du": 1,
		"er": 1, "es": 1, "war": 1, "hat": 1, "will": 1, "ja": 1, "als": 1,
		"was": 1, "wo": 1, "bis": 1, "in": 1, "im": 1, "am": 1, "an": 1,
		"so": 1, "da": 1, "mal": 1,
	},
	"pt": {
		"uma": 2, "uns": 2, "umas": 2, "eu": 2, "ele": 2, "ela": 2, "eles": 2,
		"elas": 2, "nós": 2, "você": 2, "vocês": 2, "meu": 2, "minha": 2,
		"seu": 2, "sua": 2, "isso": 2, "isto": 2, "esse": 2, "essa": 2,
		"não": 2, "porquê": 2, "quê": 2, "quem": 2, "qual": 2,
		"quando": 2, "onde": 2, "com": 2, "sem": 2, "sobre": 2, "até": 2,
		"muito": 2, "muita": 2, "mais": 2, "também": 2, "sempre": 2,
		"ninguém": 2, "alguém": 2, "tudo": 2, "agora": 2, "aqui": 2,
		"então": 2, "depois": 2, "hoje": 2, "ontem": 2, "amanhã": 2,
		"sou": 2, "és": 2, "é": 2, "somos": 2, "são": 2, "estou": 2,
		"estão": 2, "foi": 2, "foram": 2, "tenho": 2, "tem": 2, "têm": 2,
		"quero": 2, "posso": 2, "pode": 2, "vou": 2, "vai": 2, "vão": 2,
		"olá": 2, "obrigado": 2, "obrigada": 2, "bom": 2, "boa": 2, "bem": 2,
		"lhe": 2,
		"sim": 1, "o": 1, "os": 1, "as": 1, "do": 1, "da": 1, "dos": 1,
		"das": 1, "no": 1, "na": 1, "nos": 1, "nas": 1, "ao": 1, "aos": 1,
		"tu": 1, "este": 1, "esta": 1, "mas": 1, "porque": 1, "como": 1,
		"que": 1, "para": 1, "por": 1, "entre": 1, "menos": 1, "nunca": 1,
		"nada": 1, "algo": 1, "todos": 1, "antes": 1, "está": 1, "vamos": 1,
		"e": 1, "ou": 1, "em": 1, "de": 1, "a": 1, "se": 1, "me": 1, "te": 1,
	},
	"it": {
		"gli": 2, "della": 2, "degli": 2, "delle": 2, "nel": 2, "nella": 2,
		"io": 2, "lei": 2, "noi": 2, "voi": 2, "loro": 2, "mio": 2, "mia": 2,
		"tuo": 2, "tua": 2, "suo": 2, "questo": 2, "questa": 2, "quello": 2,
		"quella": 2, "che": 2, "chi": 2, "cosa": 2, "dove": 2, "quando": 2,
		"perché": 2, "senza": 2, "tra": 2, "fra": 2, "molto": 2,
		"più": 2, "anche": 2, "sempre": 2, "mai": 2, "niente": 2, "nulla": 2,
		"tutto": 2, "tutti": 2, "adesso": 2, "qui": 2, "allora": 2, "dopo": 2,
		"oggi": 2, "domani": 2, "ieri": 2, "sono": 2, "sei": 2, "è": 2,
		"siamo": 2, "siete": 2, "hai": 2, "abbiamo": 2, "hanno": 2,
		"voglio": 2, "può": 2, "vado": 2, "vai": 2, "andiamo": 2, "ciao": 2,
		"grazie": 2, "prego": 2, "buono": 2, "bene": 2, "sì": 2,
		"per": 1, "il": 1, "lo": 1, "la": 1, "i": 1, "le": 1, "un": 1, "uno": 1,
		"una": 1, "del": 1, "tu": 1, "lui": 1, "sua": 1, "come": 1, "con": 1,
		"meno": 1, "ora": 1, "prima": 1, "posso": 1,
		"non": 1, "ma": 1, "o": 1, "e": 1, "in": 1, "di": 1, "a": 1, "da": 1,
		"mi": 1, "ti": 1, "si": 1, "ci": 1, "vi": 1, "ne": 1, "era": 1,
	},
	"nl": {
		"het": 2, "een": 2, "ik": 2, "jij": 2, "hij": 2, "zij": 2, "ze": 2,
		"wij": 2, "jullie": 2, "mij": 2, "jou": 2, "haar": 2,
		"ons": 2, "mijn": 2, "jouw": 2, "zijn": 2, "dit": 2, "dat": 2,
		"deze": 2, "wat": 2, "wie": 2, "waar": 2, "wanneer": 2, "waarom": 2,
		"hoe": 2, "maar": 2, "omdat": 2, "niet": 2, "geen": 2, "ook": 2,
		"nog": 2, "wel": 2, "erg": 2, "zeer": 2, "altijd": 2,
		"nooit": 2, "niets": 2, "iets": 2, "voor": 2, "naar": 2,
		"uit": 2, "aan": 2, "bij": 2, "tegen": 2, "zonder": 2, "tussen": 2,
		"bent": 2, "heb": 2, "hebt": 2, "heeft": 2, "hebben": 2, "kan": 2,
		"moet": 2, "wil": 2, "zal": 2, "zou": 2, "nee": 2, "dank": 2,
		"alsjeblieft": 2, "goed": 2,
		"hem": 1, "heel": 1, "met": 1, "de": 1, "en": 1, "van": 1, "je": 1,
		"we": 1, "me": 1, "die": 1, "want": 1, "als": 1, "dan": 1, "al": 1,
		"hier": 1, "nu": 1, "alles": 1, "op": 1, "ben": 1, "is": 1, "was": 1,
		"waren": 1, "ja": 1, "hallo": 1, "of": 1, "in": 1, "om": 1, "te": 1,
		"er": 1,
	},
	"pl": {
		"nie": 2, "tak": 2, "jest": 2, "są": 2, "być": 2, "mam": 2,
		"masz": 2, "ty": 2, "ona": 2, "ono": 2, "wy": 2, "oni": 2, "mnie": 2,
		"jej": 2, "mój": 2, "moja": 2, "moje": 2, "twój": 2, "kto": 2,
		"gdzie": 2, "kiedy": 2, "dlaczego": 2, "jak": 2, "że": 2, "żeby": 2,
		"gdy": 2, "czy": 2, "bardzo": 2, "już": 2, "jeszcze": 2, "tylko": 2,
		"zawsze": 2, "nigdy": 2, "nic": 2, "coś": 2, "wszystko": 2,
		"teraz": 2, "tutaj": 2, "tam": 2, "dobrze": 2, "źle": 2, "proszę": 2,
		"dziękuję": 2, "cześć": 2, "przepraszam": 2, "dla": 2, "bez": 2,
		"przez": 2, "przed": 2, "się": 2,
		"to": 1, "ma": 1, "ja": 1, "on": 1, "my": 1, "one": 1, "ci": 1,
		"go": 1, "nas": 1, "was": 1, "ich": 1, "ten": 1, "ta": 1, "co": 1,
		"ale": 1, "bo": 1, "tu": 1, "po": 1, "do": 1, "od": 1, "na": 1,
		"w": 1, "z": 1, "i": 1, "a": 1, "o": 1, "u": 1,
	},
	"tr": {
		"ve": 2, "bir": 2, "bu": 2, "şu": 2, "biz": 2, "siz": 2, "onlar": 2,
		"benim": 2, "senin": 2, "bizim": 2, "kim": 2, "nerede": 2, "neden": 2,
		"niye": 2, "nasıl": 2, "ama": 2, "fakat": 2, "çünkü": 2, "eğer": 2,
		"için": 2, "ile": 2, "gibi": 2, "kadar": 2, "çok": 2, "daha": 2,
		"hiç": 2, "şey": 2, "var": 2, "yok": 2, "değil": 2, "evet": 2,
		"hayır": 2, "tamam": 2, "lütfen": 2, "teşekkür": 2, "merhaba": 2,
		"iyi": 2, "kötü": 2, "şimdi": 2, "burada": 2, "orada": 2, "sonra": 2,
		"önce": 2, "bugün": 2, "yarın": 2, "dün": 2, "mı": 2, "mü": 2,
		"o": 1, "ben": 1, "sen": 1, "ne": 1, "en": 1, "her": 1, "mi": 1,
		"mu": 1, "da": 1, "de": 1, "ki": 1, "su": 1,
	},
	"sv": {
		"och": 2, "att": 2, "det": 2, "är": 2, "jag": 2, "han": 2, "hon": 2,
		"mig": 2, "sig": 2, "oss": 2, "mitt": 2, "ditt": 2,
		"vår": 2, "vad": 2, "vem": 2, "när": 2, "varför": 2, "hur": 2,
		"inte": 2, "ej": 2, "eller": 2, "för": 2, "som": 2, "med": 2,
		"utan": 2, "från": 2, "på": 2, "har": 2, "hade": 2, "kan": 2,
		"ska": 2, "skulle": 2, "vill": 2, "måste": 2, "mycket": 2,
		"alltid": 2, "aldrig": 2, "inget": 2, "något": 2, "allt": 2,
		"här": 2, "där": 2, "tack": 2, "hej": 2, "nej": 2, "också": 2,
		"redan": 2, "bara": 2,
		"dig": 1, "du": 1, "vi": 1, "ni": 1, "de": 1, "dem": 1, "er": 1,
		"min": 1, "din": 1, "var": 1, "men": 1, "om": 1, "till": 1, "av": 1,
		"i": 1, "lite": 1, "nu": 1, "bra": 1, "ja": 1,
	},
	"ro": {
		"și": 2, "în": 2, "pentru": 2, "că": 2, "să": 2, "tine": 2,
		"mea": 2, "tău": 2, "este": 2, "sunt": 2, "ești": 2, "suntem": 2,
		"fost": 2, "avem": 2, "aveți": 2, "poate": 2, "vreau": 2,
		"trebuie": 2, "știu": 2, "cine": 2, "când": 2, "unde": 2, "dacă": 2,
		"într": 2, "către": 2, "foarte": 2, "bine": 2, "așa": 2, "ceva": 2,
		"nimic": 2, "toți": 2, "acum": 2, "aici": 2, "acolo": 2, "mult": 2,
		"puțin": 2, "mulțumesc": 2, "bună": 2,
		"ea": 1, "cu": 1, "pe": 1, "la": 1, "de": 1, "din": 1, "nu": 1, "da": 1,
		"eu": 1, "tu": 1, "el": 1, "noi": 1, "voi": 1, "ei": 1, "ele": 1,
		"mine": 1, "meu": 1, "era": 1, "am": 1, "ai": 1, "are": 1, "au": 1,
		"pot": 1, "ce": 1, "care": 1, "cum": 1, "dar": 1, "sau": 1, "tot": 1,
		"mai": 1, "te": 1, "se": 1, "mi": 1, "o": 1, "un": 1,
	},
}

type scriptCounts struct {
	latin      int
	cyrillic   int
	han        int
	kana       int
	hangul     int
	arabic     int
	hebrew     int
	greek      int
	thai       int
	devanagari int
}

func countScripts(s string) scriptCounts {
	var c scriptCounts
	for _, r := range s {
		switch {
		case unicode.Is(unicode.Latin, r):
			c.latin++
		case unicode.Is(unicode.Cyrillic, r):
			c.cyrillic++
		case unicode.Is(unicode.Hiragana, r) || unicode.Is(unicode.Katakana, r):
			c.kana++
		case unicode.Is(unicode.Han, r):
			c.han++
		case unicode.Is(unicode.Hangul, r):
			c.hangul++
		case unicode.Is(unicode.Arabic, r):
			c.arabic++
		case unicode.Is(unicode.Hebrew, r):
			c.hebrew++
		case unicode.Is(unicode.Greek, r):
			c.greek++
		case unicode.Is(unicode.Thai, r):
			c.thai++
		case unicode.Is(unicode.Devanagari, r):
			c.devanagari++
		}
	}
	return c
}

func (c scriptCounts) nonLatinTotal() int {
	return c.cyrillic + c.han + c.kana + c.hangul + c.arabic + c.hebrew + c.greek + c.thai + c.devanagari
}

func (c scriptCounts) dominantNonLatin() string {
	bestLang, bestCount := "", 0
	pick := func(lang string, count int) {
		if count > bestCount {
			bestLang, bestCount = lang, count
		}
	}
	pick("ru", c.cyrillic)
	if c.kana > 0 {
		pick("ja", c.kana+c.han)
	} else {
		pick("zh", c.han)
	}
	pick("ko", c.hangul)
	pick("ar", c.arabic)
	pick("he", c.hebrew)
	pick("el", c.greek)
	pick("th", c.thai)
	pick("hi", c.devanagari)
	return bestLang
}

func (c scriptCounts) targetLetters(target string) int {
	switch target {
	case "ru", "uk", "bg":
		return c.cyrillic
	case "ja":
		return c.kana + c.han
	case "zh":
		return c.han
	case "ko":
		return c.hangul
	case "ar", "fa", "ur":
		return c.arabic
	case "he":
		return c.hebrew
	case "el":
		return c.greek
	case "th":
		return c.thai
	case "hi":
		return c.devanagari
	}
	return 0
}

// tokenizeWords splits s into lowercase words, keeping internal apostrophes
// and preserving diacritics.
func tokenizeWords(s string) (words []string, latinWords int) {
	runes := []rune(s)
	var cur []rune
	hasLatin := false
	flush := func() {
		if len(cur) == 0 {
			return
		}
		words = append(words, string(cur))
		if hasLatin {
			latinWords++
		}
		cur = cur[:0]
		hasLatin = false
	}
	for i, r := range runes {
		if unicode.IsLetter(r) {
			cur = append(cur, unicode.ToLower(r))
			if unicode.Is(unicode.Latin, r) {
				hasLatin = true
			}
			continue
		}
		if (r == '\'' || r == '’') && len(cur) > 0 && i+1 < len(runes) && unicode.IsLetter(runes[i+1]) {
			cur = append(cur, '\'')
			continue
		}
		flush()
	}
	flush()
	return words, latinWords
}

func scoreLatin(clean string, words []string) (scores, distinctHits map[string]int, hasUniquePunct map[string]bool) {
	scores = make(map[string]int, len(scoredLatinLangs))
	distinctHits = make(map[string]int, len(scoredLatinLangs))
	hasUniquePunct = make(map[string]bool, 2)
	if strings.ContainsAny(clean, "¿¡") {
		scores["es"] += 3
		hasUniquePunct["es"] = true
	}
	if strings.ContainsRune(clean, 'ñ') || strings.ContainsRune(clean, 'Ñ') {
		scores["es"] += 2
	}
	if strings.ContainsRune(clean, 'ß') {
		scores["de"] += 3
		hasUniquePunct["de"] = true
	}
	if strings.ContainsRune(clean, 'œ') || strings.ContainsRune(clean, 'Œ') {
		scores["fr"] += 2
	}
	if strings.ContainsAny(clean, "ãõÃÕ") {
		scores["pt"] += 2
	}
	if strings.ContainsAny(clean, "łąęśźżńŁĄĘŚŹŻŃ") {
		scores["pl"] += 2
	}
	if strings.ContainsAny(clean, "ğşıĞŞ") {
		scores["tr"] += 2
	}
	seen := make(map[string]bool, len(words))
	for _, w := range words {
		if seen[w] {
			continue
		}
		seen[w] = true
		for _, lang := range scoredLatinLangs {
			if wt := latinStopwords[lang][w]; wt > 0 {
				scores[lang] += wt
				distinctHits[lang]++
			}
		}
	}
	return scores, distinctHits, hasUniquePunct
}

func cueGapMS(a, b subtext.Cue) int64 {
	if a.EndMS <= b.StartMS {
		return b.StartMS - a.EndMS
	}
	if b.EndMS <= a.StartMS {
		return a.StartMS - b.EndMS
	}
	return 0
}

// detectForeignCues identifies cues whose spoken language differs from
// targetLang, returning a map from cue index to ISO 639-1 source code.
func detectForeignCues(cues []subtext.Cue, targetLang string) map[int]string {
	target := baseOf(NormLanguage(targetLang))
	if target == "" || len(cues) == 0 {
		return nil
	}
	isLatinTarget := latinTargetLangs[target]
	isNonLatinTarget := nonLatinTargetLangs[target]
	if !isLatinTarget && !isNonLatinTarget {
		return nil
	}

	n := len(cues)
	flagged := make(map[int]string)
	anchors := make([]string, n)
	wordCounts := make([]int, n)
	targetScores := make([]int, n)

	for i, c := range cues {
		clean := cueTagRe.ReplaceAllString(c.Text, " ")
		sc := countScripts(clean)
		words, latinWords := tokenizeWords(clean)
		wordCounts[i] = len(words)

		if isNonLatinTarget {
			if latinWords >= 2 && sc.latin > 2*sc.targetLetters(target) {
				scores, _, _ := scoreLatin(clean, words)
				bestLang, bestScore := "", 0
				for _, lang := range scoredLatinLangs {
					if scores[lang] > bestScore {
						bestLang, bestScore = lang, scores[lang]
					}
				}
				if bestScore >= 2 {
					flagged[i] = bestLang
				} else if latinWords >= 3 {
					flagged[i] = "en"
				}
			}
			continue
		}

		// Target uses the Latin script: check non-Latin script first.
		nonLatin := sc.nonLatinTotal()
		if nonLatin >= 2 && (nonLatin >= sc.latin || nonLatin >= 3) {
			if lang := sc.dominantNonLatin(); lang != "" {
				flagged[i] = lang
				continue
			}
		}

		scores, distinctHits, hasUniquePunct := scoreLatin(clean, words)
		targetScores[i] = scores[target]
		bestLang, bestScore := "", 0
		for _, lang := range scoredLatinLangs {
			if lang == target {
				continue
			}
			if scores[lang] > bestScore {
				bestLang, bestScore = lang, scores[lang]
			}
		}
		multiHit := distinctHits[bestLang] >= 2 &&
			((bestScore >= 3 && bestScore > targetScores[i]) ||
				(bestScore >= 2 && targetScores[i] == 0))
		uniquePunct := hasUniquePunct[bestLang] && bestScore >= 3 && targetScores[i] <= bestScore
		if multiHit || uniquePunct {
			flagged[i] = bestLang
			anchors[i] = bestLang
		}
	}

	if isLatinTarget {
		pass1 := make([]string, n)
		for i := 0; i < n; i++ {
			if flagged[i] != "" || targetScores[i] != 0 || wordCounts[i] < 1 || wordCounts[i] > 5 {
				continue
			}
			pass1[i] = neighborLang(cues, anchors, i, 6000)
		}
		for i, lang := range pass1 {
			if lang != "" {
				flagged[i] = lang
			}
		}
		pass2 := make([]string, n)
		for i := 0; i < n; i++ {
			if flagged[i] != "" || targetScores[i] != 0 || wordCounts[i] < 1 || wordCounts[i] > 3 {
				continue
			}
			pass2[i] = neighborLang(cues, pass1, i, 3000)
		}
		for i, lang := range pass2 {
			if lang != "" {
				flagged[i] = lang
			}
		}
	}

	if len(flagged) == 0 {
		return nil
	}
	return flagged
}

func neighborLang(cues []subtext.Cue, source []string, i int, maxGapMS int64) string {
	var leftLang, rightLang string
	leftGap, rightGap := maxGapMS+1, maxGapMS+1
	if i > 0 && source[i-1] != "" {
		if g := cueGapMS(cues[i-1], cues[i]); g <= maxGapMS {
			leftLang, leftGap = source[i-1], g
		}
	}
	if i+1 < len(cues) && source[i+1] != "" {
		if g := cueGapMS(cues[i], cues[i+1]); g <= maxGapMS {
			rightLang, rightGap = source[i+1], g
		}
	}
	if leftLang != "" && (rightLang == "" || leftGap <= rightGap) {
		return leftLang
	}
	return rightLang
}

// translateForeignCues translates any cues spoken in a language other than
// lang using the configured translator, updating cues in place. It is a
// best-effort pass and never fails transcription.
func (s *Service) translateForeignCues(ctx context.Context, fc *fileCtx, lang string, cues []subtext.Cue) int {
	flagged := detectForeignCues(cues, lang)
	if len(flagged) == 0 {
		return 0
	}
	c, tr, err := s.translatorClient(ctx)
	if err != nil {
		return 0
	}

	groups := map[string][]int{}
	for idx, fromLang := range flagged {
		groups[fromLang] = append(groups[fromLang], idx)
	}
	langs := make([]string, 0, len(groups))
	for fromLang := range groups {
		langs = append(langs, fromLang)
	}
	slices.Sort(langs)

	targetBase := baseOf(NormLanguage(lang))
	chunk := chunkFor(tr)
	effChunk := chunk
	if effChunk <= 0 {
		effChunk = translate.DefaultChunk
	}
	translated := 0

	for _, fromLang := range langs {
		if fromLang == "" || fromLang == targetBase {
			continue
		}
		idxs := groups[fromLang]
		slices.Sort(idxs)
		lines := make([]translate.Line, len(idxs))
		for k, idx := range idxs {
			lines[k] = translate.Line{N: idx + 1, Text: cues[idx].Text}
		}
		job := translate.Job{
			Batch: translate.Batch{
				From:     fromLang,
				To:       lang,
				FromName: LanguageName(fromLang),
				ToName:   LanguageName(lang),
				Title:    fc.title.Name,
				Kind:     fc.item.Kind,
				MediaID:  fc.file.ID,
				Lines:    lines,
			},
			Chunk: chunk,
		}
		var res translate.Result
		var terr error
		if slots := slotsOf(c); slots > 1 && len(lines) > effChunk {
			res, terr = translate.RunParts(ctx, c, job, slots)
		} else {
			res, terr = translate.Run(ctx, c, job)
		}
		for k, idx := range idxs {
			if k < len(res.Done) && res.Done[k] && k < len(res.Lines) && strings.TrimSpace(res.Lines[k]) != "" {
				cues[idx].Text = res.Lines[k]
				translated++
			}
		}
		if terr != nil {
			log.Printf("subtitles: translating %s foreign cues for file %d: %v", fromLang, fc.file.ID, terr)
		}
	}
	return translated
}
