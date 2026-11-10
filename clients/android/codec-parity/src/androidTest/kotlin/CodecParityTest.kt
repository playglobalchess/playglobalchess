// Codec Android parity — instrumented test (SPEC-S2-PROTOCOL §8, PLAN-CODEC
// §4.3, NFR-10, ADR-9).
//
// Runs the SAME parity assertions as platform/mobile/KotlinCodecParityTest.kt,
// but as a JUnit instrumented test so it executes on an Android emulator/device
// via `./gradlew connectedAndroidTest` — where the aarch64 .so loads (it cannot
// on a host JVM). The codec .so + the generated UniFFI Kotlin bindings are
// placed by platform/mobile/build_codec_mobile.sh.
//
// Frames mirror protocol/codec-ffi/tests/codec_parity_oracle.rs and are a subset
// of protocol/vectors/vectors.json — change in lockstep (the parity contract).

package com.playglobalchess.codecparity

import androidx.test.ext.junit.runners.AndroidJUnit4
import org.junit.Assert.assertArrayEquals
import org.junit.Assert.assertThrows
import org.junit.Test
import org.junit.runner.RunWith
import uniffi.pgc_protocol_ffi.decodeClient
import uniffi.pgc_protocol_ffi.decodeEvent
import uniffi.pgc_protocol_ffi.decodeServer
import uniffi.pgc_protocol_ffi.encodeClient
import uniffi.pgc_protocol_ffi.encodeEvent
import uniffi.pgc_protocol_ffi.encodeServer

@RunWith(AndroidJUnit4::class)
class CodecParityTest {
    private val clientMovePromoHex =
        "01032466343761633130622d353863632d343337322d613536372d30653032623263336434373967056537653871"
    private val serverGameEndHex =
        "01852466343761633130622d353863632d343337322d613536372d3065303262326333643437390202000a021817"
    private val serverStatePendingHex =
        "01842466343761633130622d353863632d343337322d613536372d3065303262326333643437393b726e62716b626e722f70707070707070702f382f382f3450332f382f50505050315050502f524e42514b424e522062204b516b7120653320302031010290a10fa0ef0f0112020000046731663300011201f483cea6a532"
    private val eventRatingAppliedHex =
        "2967616d652f66343761633130622d353863632d343337322d613536372d3065303262326333643437390c0c008080cea6a5320e626c69747a2d7374616e646172640200b817d0173c8fc2753d01b817a0173c8fc2753d"

    private fun fromHex(h: String): ByteArray =
        ByteArray(h.length / 2) {
            ((h[it * 2].digitToInt(16) shl 4) or h[it * 2 + 1].digitToInt(16)).toByte()
        }

    @Test
    fun clientDecodeReencodeIdentity() {
        val b = fromHex(clientMovePromoHex)
        assertArrayEquals(b, encodeClient(decodeClient(b)))
    }

    @Test
    fun serverDecodeReencodeIdentity() {
        val b = fromHex(serverGameEndHex)
        assertArrayEquals(b, encodeServer(decodeServer(b)))
    }

    /** §4A v1.3 STATE: read the new fields through the binding, then round-trip. */
    @Test
    fun stateV13FieldsAndIdentity() {
        val b = fromHex(serverStatePendingHex)
        val msg = decodeServer(b)
        val body = (msg as uniffi.pgc_protocol_ffi.ServerMsg.State).state
        org.junit.Assert.assertEquals(1.toUByte(), body.youSeat)
        org.junit.Assert.assertEquals(18u, body.ply)
        org.junit.Assert.assertEquals(2.toUByte(), body.status)
        org.junit.Assert.assertEquals("g1f3", body.lastMoveUci)
        org.junit.Assert.assertEquals(0.toUByte(), body.pendingDrawBy)
        org.junit.Assert.assertEquals(1.toUByte(), body.takebackBy)
        org.junit.Assert.assertEquals(18u, body.takebackAtPly)
        org.junit.Assert.assertEquals(1.toUByte(), body.runningSeat)
        assertArrayEquals(b, encodeServer(msg))
    }

    @Test
    fun eventDecodeReencodeIdentity() {
        val b = fromHex(eventRatingAppliedHex)
        assertArrayEquals(b, encodeEvent(decodeEvent(b)))
    }

    @Test
    fun malformedFrameThrowsNotCrash() {
        assertThrows(Exception::class.java) { decodeClient(ByteArray(0)) }
    }
}
