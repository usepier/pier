//! What's typed in pier is code, commands, branch names and prompts:
//! macOS's spelling correction, smart quotes and dashes, capitalization and
//! text replacement change it. WebKit reads these switches from the app's
//! own defaults, which come before the system-wide setting — so they're
//! turned off here, for this app only, before the web view starts.

use objc2_foundation::{NSString, NSUserDefaults};

const OFF: &[&str] = &[
    "WebAutomaticSpellingCorrectionEnabled",
    "WebContinuousSpellCheckingEnabled",
    "WebGrammarCheckingEnabled",
    "WebAutomaticQuoteSubstitutionEnabled",
    "WebAutomaticDashSubstitutionEnabled",
    "WebAutomaticTextReplacementEnabled",
    "NSAutomaticSpellingCorrectionEnabled",
    "NSAutomaticQuoteSubstitutionEnabled",
    "NSAutomaticDashSubstitutionEnabled",
    "NSAutomaticTextReplacementEnabled",
    "NSAutomaticCapitalizationEnabled",
    "NSAutomaticPeriodSubstitutionEnabled",
];

pub fn plain() {
    let defaults = NSUserDefaults::standardUserDefaults();
    for key in OFF {
        defaults.setBool_forKey(false, &NSString::from_str(key));
    }
}
