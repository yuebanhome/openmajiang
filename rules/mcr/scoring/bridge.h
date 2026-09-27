#ifndef OPENMAJIANG_MCR_SCORING_BRIDGE_H
#define OPENMAJIANG_MCR_SCORING_BRIDGE_H

#include <stdint.h>

#ifdef __cplusplus
extern "C" {
#endif

typedef struct {
    uint8_t kind;
    uint8_t tile;
    uint8_t offer;
} om_mcr_pack;

typedef struct {
    uint8_t hand[13];
    int hand_count;
    om_mcr_pack packs[4];
    int pack_count;
    uint8_t win_tile;
    uint8_t flowers;
    uint8_t flags;
    uint8_t seat_wind;
    uint8_t round_wind;
} om_mcr_input;

typedef struct {
    int total;
    int counts[82];
    int form;
    uint16_t packs[5];
    int pack_count;
    uint8_t knitted[9];
} om_mcr_result;

int om_mcr_evaluate(const om_mcr_input *input, om_mcr_result *result);
const char *om_mcr_fan_name(int official_id);
int om_mcr_fan_points(int official_id);
int om_mcr_shanten(const uint8_t hand[13], int count, int meld_count, uint8_t useful[128]);

#ifdef __cplusplus
}
#endif
#endif
