#include "bridge.h"
#include "vendor/mahjong-algorithm/fan_calculator.cpp"
#include "vendor/mahjong-algorithm/shanten.cpp"

// The upstream enum places Two Concealed Kongs among its six-point fans.
// Public IDs always follow the selected WMO 2014 edition (1..81).
static int upstream_id(int official) {
    if (official == 48) return mahjong::TWO_CONCEALED_KONGS;
    if (official >= 49 && official <= 53) return official - 1;
    return official;
}

static_assert(mahjong::FAN_TABLE_SIZE == 82, "Only the 81 WMO fan types are enabled");

extern "C" int om_mcr_evaluate(const om_mcr_input *input, om_mcr_result *result) {
    try {
        mahjong::calculate_param_t param{};
        param.hand_tiles.tile_count = input->hand_count;
        param.hand_tiles.pack_count = input->pack_count;
        for (int i = 0; i < input->hand_count; ++i) {
            param.hand_tiles.standing_tiles[i] = input->hand[i];
        }
        for (int i = 0; i < input->pack_count; ++i) {
            const om_mcr_pack &p = input->packs[i];
            param.hand_tiles.fixed_packs[i] = mahjong::make_pack(p.offer, p.kind, p.tile);
        }
        param.win_tile = input->win_tile;
        param.flower_count = input->flowers;
        param.win_flag = input->flags;
        param.seat_wind = static_cast<mahjong::wind_t>(input->seat_wind);
        param.prevalent_wind = static_cast<mahjong::wind_t>(input->round_wind);
        mahjong::fan_table_t fans{};
        mahjong::calculation_details_t details{};
        int points = mahjong::calculate_fan(&param, &fans, &details);
        if (points < 0) return points;
        result->total = points;
        for (int i = 1; i <= 81; ++i) result->counts[i] = fans[upstream_id(i)];
        result->form = details.form;
        result->pack_count = details.pack_count;
        std::memcpy(result->packs, details.packs, sizeof(result->packs));
        std::memcpy(result->knitted, details.knitted, sizeof(result->knitted));
        return 0;
    } catch (...) {
        return -100;
    }
}

extern "C" const char *om_mcr_fan_name(int official) {
    if (official < 1 || official > 81) return "";
    if (official == 77) return "边张";
    if (official == 78) return "坎张";
    if (official == 79) return "单调将";
    return mahjong::fan_name<>::text[upstream_id(official)];
}

extern "C" int om_mcr_fan_points(int official) {
    if (official < 1 || official > 81) return 0;
    return mahjong::fan_value<>::table[upstream_id(official)];
}

extern "C" int om_mcr_shanten(const uint8_t hand[13], int count, int meld_count, uint8_t useful[128]) {
    try {
        int best = 99;
        auto consider = [&](int (*fn)(const mahjong::tile_t *, intptr_t, mahjong::useful_table_t *)) {
            mahjong::useful_table_t candidates{};
            int distance = fn(hand, count, &candidates);
            if (distance > best) return;
            if (distance < best) {
                best = distance;
                std::memset(useful, 0, 128);
            }
            for (int t = 0; t < mahjong::TILE_TABLE_SIZE; ++t) {
                if (candidates[t]) useful[t] = 1;
            }
        };
        consider(mahjong::regular_shanten);
        if (meld_count == 0) {
            consider(mahjong::seven_pairs_shanten);
            consider(mahjong::thirteen_orphans_shanten);
            consider(mahjong::honors_and_knitted_tiles_shanten);
        }
        if (meld_count <= 1) consider(mahjong::knitted_straight_shanten);
        return best;
    } catch (...) {
        return -100;
    }
}
