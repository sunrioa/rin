package io.github.sunrioa.rin.companion;

import net.fabricmc.fabric.api.gametest.v1.GameTest;
import net.minecraft.core.BlockPos;
import net.minecraft.server.level.ServerLevel;
import net.minecraft.server.permissions.PermissionSet;
import net.minecraft.util.ProblemReporter;
import net.minecraft.world.entity.EntitySpawnReason;
import net.minecraft.world.entity.player.Player;
import net.minecraft.world.level.GameType;
import net.minecraft.world.level.storage.TagValueInput;
import net.minecraft.world.level.storage.TagValueOutput;
import net.minecraft.world.phys.Vec3;
import net.minecraft.gametest.framework.GameTestHelper;

import java.util.UUID;

public final class CompanionGameTest {
    private CompanionGameTest() {
    }

    @GameTest(maxTicks = 40)
    public static void companionIdentityAndFollow(GameTestHelper helper) {
        ServerLevel level = helper.getLevel();
        UUID ownerId = UUID.fromString("00000000-0000-0000-0000-000000000011");
        UUID companionId = UUID.fromString("00000000-0000-0000-0000-000000000012");
        CompanionEntity entity = CompanionEntities.TYPE.create(level, EntitySpawnReason.COMMAND);
        entity.setUUID(companionId);
        entity.setOwnerId(ownerId);
        entity.setMode(CompanionEntity.Mode.FOLLOW);
        entity.setPos(new Vec3(2, 2, 2));
        TagValueOutput output = TagValueOutput.createWithoutContext(ProblemReporter.DISCARDING);
        entity.saveWithoutId(output);
        CompanionEntity restored = CompanionEntities.TYPE.create(level, EntitySpawnReason.COMMAND);
        restored.load(TagValueInput.create(ProblemReporter.DISCARDING, level.registryAccess(), output.buildResult()));
        helper.assertTrue(ownerId.equals(restored.ownerId()), "owner identity was not persisted");
        helper.assertTrue(companionId.equals(restored.getUUID()), "companion identity was not persisted");
        CompanionRuntime runtime = CompanionRuntime.forServer(level.getServer());
        helper.assertTrue(runtime.call(() -> level.getServer().isSameThread()).join(),
                "runtime call escaped the server thread");
        helper.assertTrue(runtime.spawn(restored), "duplicate companion identity was accepted");
        Player owner = helper.makeMockServerPlayer(GameType.SURVIVAL);
        owner.setUUID(ownerId);
        owner.setPos(restored.position().add(6, 0, 0));
        restored.customServerAiStep(level);
        helper.assertTrue(restored.getNavigation().isInProgress(), "follow mode did not start navigation");
        CompanionEntity duplicate = CompanionEntities.TYPE.create(level, EntitySpawnReason.COMMAND);
        duplicate.setUUID(UUID.fromString(companionId.toString()));
        helper.assertTrue(!runtime.spawn(duplicate), "duplicate companion identity was accepted");
        restored.setMode(CompanionEntity.Mode.STOPPED);
        helper.assertTrue(!restored.getNavigation().isInProgress(), "stop mode left navigation active");
        UUID commandOwner = UUID.fromString("00000000-0000-0000-0000-000000000021");
        helper.assertTrue(runtime.spawnFor(commandOwner, level, new Vec3(3, 2, 3)),
                "owner could not spawn a companion");
        helper.assertTrue(!runtime.spawnFor(commandOwner, level, new Vec3(4, 2, 4)),
                "owner spawned a second companion");
        helper.assertTrue(!runtime.setMode(UUID.randomUUID(), CompanionEntity.Mode.FOLLOW),
                "non-owner changed companion mode");
        helper.assertTrue(runtime.setMode(commandOwner, CompanionEntity.Mode.FOLLOW),
                "owner could not resume companion");
        helper.assertTrue(!CompanionCommands.canConfigure(PermissionSet.NO_PERMISSIONS),
                "model configuration accepted a non-admin");
        helper.assertTrue(CompanionCommands.canConfigure(PermissionSet.ALL_PERMISSIONS),
                "model configuration rejected an administrator");
        helper.succeed();
    }
}
